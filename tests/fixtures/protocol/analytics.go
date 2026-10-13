package protocol

import (
	"context"
	"errors"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

var (
	ErrFaultConflict        = errors.New("fault conflict")
	ErrUnsupportedOperation = errors.New("unsupported operation")
	ErrAnalyticsFault       = errors.New("post-prepare calculation unavailable")
)

const AnalyticsPoint = "warehouse-after-prepare-before-analytics"

type FaultArm struct {
	FaultID       string `json:"faultID"`
	Point         string `json:"point"`
	OperationID   string `json:"operationID"`
	CommandID     string `json:"commandID"`
	ExpireAfterMs int    `json:"expireAfterMs"`
	Count         int    `json:"count"`
}
type FaultState struct {
	FaultID      string `json:"faultID"`
	State        string `json:"state"`
	Hits         int    `json:"hits"`
	OwnerOutcome string `json:"ownerOutcome"`
}
type AnalyticsFault = OwnerFaults

func NewAnalyticsFault(timer func() time.Time) (*AnalyticsFault, error) {
	if timer == nil {
		return nil, contract.ErrInvalidArgument
	}
	return NewOwnerFaults(timer), nil
}
func (f *OwnerFaults) Calculate(ctx context.Context, operation, command string, p contract.StockPreparation, calculate func(context.Context, contract.StockPreparation) (contract.InventoryView, error)) (contract.InventoryView, error) {
	if err := ctx.Err(); err != nil {
		return contract.InventoryView{}, err
	}
	if calculate == nil {
		return contract.InventoryView{}, contract.ErrInvalidArgument
	}
	outcome := "not-observed"
	if p.InvalidationRequired {
		outcome = "committed"
	}
	if f.hit(operation, command, AnalyticsPoint, outcome) {
		return contract.InventoryView{}, ErrAnalyticsFault
	}
	return calculate(ctx, p)
}
