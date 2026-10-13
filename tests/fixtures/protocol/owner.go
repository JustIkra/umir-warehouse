package protocol

import (
	"context"
	"sync"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
)

type ownerFault struct {
	arm   FaultArm
	state FaultState
	until time.Time
}
type OwnerFaults struct {
	mu     sync.Mutex
	timer  func() time.Time
	faults map[string]*ownerFault
}

func NewOwnerFaults(timer func() time.Time) *OwnerFaults {
	return &OwnerFaults{timer: timer, faults: make(map[string]*ownerFault)}
}
func (f *OwnerFaults) expire(v *ownerFault) {
	if v.state.State == "armed" && !f.timer().Before(v.until) {
		v.state.State = "expired"
	}
}
func (f *OwnerFaults) Arm(input FaultArm) (FaultState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !contract.ValidEntityID(input.FaultID) || input.CommandID == "" || len(input.CommandID) > 128 || input.ExpireAfterMs < 1 || input.ExpireAfterMs > 30000 || input.Count != 1 {
		return FaultState{}, contract.ErrInvalidArgument
	}
	supported := input.Point == AnalyticsPoint && (input.OperationID == "inventory" || input.OperationID == "analyticsstock")
	if input.Point == "before-commit" || input.Point == "after-real-commit-return-unknown" || input.Point == "after-durable-before-reply" {
		supported = input.OperationID == "registerProduct" || input.OperationID == "defineDish" || input.OperationID == "receiveDelivery" || input.OperationID == "confirmWriteoff" || input.OperationID == "commitOrder"
	}
	if !supported {
		return FaultState{}, ErrUnsupportedOperation
	}
	for _, v := range f.faults {
		f.expire(v)
		if v.state.State == "armed" {
			return FaultState{}, ErrFaultConflict
		}
	}
	if old, exists := f.faults[input.FaultID]; exists && old.state.State != "cleared" {
		return FaultState{}, ErrFaultConflict
	}
	if len(f.faults) >= 256 {
		return FaultState{}, contract.ErrLimitExceeded
	}
	v := &ownerFault{arm: input, until: f.timer().Add(time.Duration(input.ExpireAfterMs) * time.Millisecond), state: FaultState{FaultID: input.FaultID, State: "armed", OwnerOutcome: "not-observed"}}
	f.faults[input.FaultID] = v
	return v.state, nil
}
func (f *OwnerFaults) Read(id string) (FaultState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.faults[id]
	if !ok {
		return FaultState{}, contract.ErrInvalidArgument
	}
	f.expire(v)
	return v.state, nil
}
func (f *OwnerFaults) Clear(id string) (FaultState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.faults[id]
	if !ok {
		return FaultState{}, contract.ErrInvalidArgument
	}
	f.expire(v)
	v.state.State = "cleared"
	return v.state, nil
}
func (f *OwnerFaults) hit(operation, command, point, outcome string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.faults {
		f.expire(v)
		if v.state.State == "armed" && v.arm.Point == point && v.arm.OperationID == operation && v.arm.CommandID == command {
			v.state.State = "hit"
			v.state.Hits = 1
			v.state.OwnerOutcome = outcome
			return true
		}
	}
	return false
}

type Directory struct {
	*bootstrap.Registry
	faults *OwnerFaults
}

func NewDirectory(r *bootstrap.Registry, f *OwnerFaults) *Directory { return &Directory{r, f} }
func (d *Directory) RuntimeFor(ctx context.Context, s contract.Scope) (bootstrap.ScopedRuntime, error) {
	r, err := d.Resolve(ctx, s)
	if err != nil {
		return nil, err
	}
	return &decoratedRuntime{Runtime: r, faults: d.faults}, nil
}

type decoratedRuntime struct {
	*bootstrap.Runtime
	faults *OwnerFaults
}

func (r *decoratedRuntime) mutate(ctx context.Context, operation, command string, call func() error) error {
	_, found, err := r.FindRecordedCommand(ctx, command)
	if err != nil {
		return err
	}
	if !found && r.faults.hit(operation, command, "before-commit", "not_committed") {
		return contract.ErrInvalidArgument
	}
	err = call()
	if err != nil {
		return err
	}
	_, found, lookupErr := r.FindRecordedCommand(ctx, command)
	if lookupErr != nil {
		return contract.ErrUnknownWriteOutcome
	}
	if !found {
		return nil
	}
	if r.faults.hit(operation, command, "after-real-commit-return-unknown", "committed") {
		return contract.ErrUnknownWriteOutcome
	}
	if r.faults.hit(operation, command, "after-durable-before-reply", "committed") {
		if state, ok := ctx.Value(requestStateKey{}).(*requestState); ok {
			state.drop = true
		} else {
			return contract.ErrUnknownWriteOutcome
		}
	}
	return nil
}
func (r *decoratedRuntime) RegisterProduct(ctx context.Context, q contract.RegisterProductRequest) (v contract.Product, err error) {
	err = r.mutate(ctx, "registerProduct", q.CommandID, func() error { v, err = r.Runtime.RegisterProduct(ctx, q); return err })
	return
}
func (r *decoratedRuntime) DefineDish(ctx context.Context, q contract.DefineDishRequest) (v contract.Dish, err error) {
	err = r.mutate(ctx, "defineDish", q.CommandID, func() error { v, err = r.Runtime.DefineDish(ctx, q); return err })
	return
}
func (r *decoratedRuntime) ReceiveDelivery(ctx context.Context, q contract.ReceiveDeliveryRequest) (v contract.DeliveryReceipt, err error) {
	err = r.mutate(ctx, "receiveDelivery", q.CommandID, func() error { v, err = r.Runtime.ReceiveDelivery(ctx, q); return err })
	return
}
func (r *decoratedRuntime) ConfirmWriteoff(ctx context.Context, q contract.ConfirmWriteoffRequest) (v contract.WriteoffResult, err error) {
	err = r.mutate(ctx, "confirmWriteoff", q.CommandID, func() error { v, err = r.Runtime.ConfirmWriteoff(ctx, q); return err })
	return
}
func (r *decoratedRuntime) CommitAcceptedOrder(ctx context.Context, q contract.CommitOrderRequest) (v contract.CommitResult, err error) {
	err = r.mutate(ctx, "commitOrder", q.CommandID, func() error { v, err = r.Runtime.CommitAcceptedOrder(ctx, q); return err })
	return
}
func (r *decoratedRuntime) CalculateStock(ctx context.Context, p contract.StockPreparation) (contract.InventoryView, error) {
	if state, ok := ctx.Value(requestStateKey{}).(*requestState); ok {
		outcome := "not-observed"
		if p.InvalidationRequired {
			outcome = "committed"
		}
		if r.faults.hit(state.operation, state.command, AnalyticsPoint, outcome) {
			return contract.InventoryView{}, ErrAnalyticsFault
		}
	}
	return r.Runtime.CalculateStock(ctx, p)
}
func (r *decoratedRuntime) AnalyticsStock(ctx context.Context, q contract.AnalyticsRequest) (contract.StockReport, error) {
	return r.AnalyticsStockUsing(ctx, q, r)
}

var _ bootstrap.ScopedRuntime = (*decoratedRuntime)(nil)
var _ bootstrap.ScopedDirectory = (*Directory)(nil)
