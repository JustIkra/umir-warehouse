package bootstrap

import (
	"context"
	"crypto/rand"

	"net/http"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/httpapi"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/adapters/zonedata"
	"github.com/JustIkra/umir-warehouse/internal/application"
)

type Runtime struct {
	analytics *application.Analytics
	owner     *application.Owner
	attempts  *application.OrderAttemptCoordinator
	gate      *runtimeGate
}

type attemptOwnerAdapter struct{ runtime *Runtime }

func (a attemptOwnerAdapter) CommitAcceptedOrder(ctx context.Context, request contract.CommitOrderRequest) (contract.CommitResult, error) {
	return a.runtime.owner.CommitAcceptedOrder(ctx, request)
}

func (a attemptOwnerAdapter) FindRecordedCommand(ctx context.Context, commandID string) (contract.CommandRecord, bool, error) {
	return a.runtime.findRecordedCommand(ctx, commandID)
}

func NewHTTPHandlers(runtime *Runtime, options HTTPOptions) (http.Handler, http.Handler, error) {
	if runtime == nil {
		return nil, nil, contract.ErrInvalidArgument
	}
	return httpapi.NewHandlers(runtime, httpapi.Options{RequestTimeout: options.RequestTimeout, MaxRequestBytes: options.MaxRequestBytes})
}

func NewInMemory(options Options) (*Runtime, error) {
	if options.Now == nil || options.NewID == nil || options.EventsPerSnapshot <= 0 {
		return nil, contract.ErrInvalidArgument
	}

	if err := zonedata.Check(); err != nil {
		return nil, err
	}
	if options.TZDataVersion != "" && options.TZDataVersion != zonedata.Version {
		return nil, contract.ErrTimeZoneDataUnavailable
	}
	options.TZDataVersion = zonedata.Version
	owner, err := application.NewOwner(memory.NewEventStore(), options.Now, options.NewID)
	if err != nil {
		return nil, err
	}
	if err := owner.ConfigureZoneLoader(zonedata.Load); err != nil {
		return nil, err
	}
	if err := owner.ConfigureTZData(options.TZDataVersion); err != nil {
		return nil, err
	}
	if options.Scope.RestaurantID != "" {
		if err := owner.ConfigureScope(options.Scope); err != nil {
			return nil, err
		}
	}
	if err := owner.ConfigureSnapshots(memory.NewSnapshotStore(), options.EventsPerSnapshot); err != nil {
		return nil, err
	}
	coverage := options.CoverageFrom
	if coverage.IsZero() && options.Scope.RestaurantID != "" {
		coverage = options.Now()
	}
	runtime := &Runtime{owner: owner, gate: newRuntimeGate()}
	if !coverage.IsZero() {
		runtime.analytics, err = application.NewAnalytics(runtime, owner, coverage, options.Now, rand.Text, zonedata.Load)
		if err != nil {
			return nil, err
		}
	}
	runtime.attempts = application.NewOrderAttemptCoordinator(attemptOwnerAdapter{runtime: runtime}, attemptOwnerAdapter{runtime: runtime})
	return runtime, nil
}

func (r *Runtime) Recover(ctx context.Context) error {
	if err := r.enter(ctx); err != nil {
		return err
	}
	defer r.leave()
	return r.owner.Recover(ctx)
}

func (r *Runtime) RegisterProduct(ctx context.Context, request contract.RegisterProductRequest) (contract.Product, error) {
	if err := r.enter(ctx); err != nil {
		return contract.Product{}, err
	}
	defer r.leave()
	var product contract.Product
	err := r.attempts.RunOtherCommand(ctx, request.CommandID, func() error {
		var callErr error
		product, callErr = r.owner.RegisterProduct(ctx, request)
		return callErr
	})
	return product, err
}

func (r *Runtime) DefineDish(ctx context.Context, request contract.DefineDishRequest) (contract.Dish, error) {
	if err := r.enter(ctx); err != nil {
		return contract.Dish{}, err
	}
	defer r.leave()
	var dish contract.Dish
	err := r.attempts.RunOtherCommand(ctx, request.CommandID, func() error {
		var callErr error
		dish, callErr = r.owner.DefineDish(ctx, request)
		return callErr
	})
	return dish, err
}

func (r *Runtime) ReceiveDelivery(ctx context.Context, request contract.ReceiveDeliveryRequest) (contract.DeliveryReceipt, error) {
	if err := r.enter(ctx); err != nil {
		return contract.DeliveryReceipt{}, err
	}
	defer r.leave()
	var receipt contract.DeliveryReceipt
	err := r.attempts.RunOtherCommand(ctx, request.CommandID, func() error {
		var callErr error
		receipt, callErr = r.owner.ReceiveDelivery(ctx, request)
		return callErr
	})
	return receipt, err
}

func (r *Runtime) EvaluateOrder(ctx context.Context, request contract.EvaluateOrderRequest) (contract.OrderEvaluation, error) {
	if err := r.enter(ctx); err != nil {
		return contract.OrderEvaluation{}, err
	}
	defer r.leave()
	return r.owner.EvaluateOrder(ctx, request)
}

func (r *Runtime) EvaluateMenu(ctx context.Context) (contract.MenuEvaluation, error) {
	if err := r.enter(ctx); err != nil {
		return contract.MenuEvaluation{}, err
	}
	defer r.leave()
	return r.owner.EvaluateMenu(ctx)
}

func (r *Runtime) CommitAcceptedOrder(ctx context.Context, request contract.CommitOrderRequest) (contract.CommitResult, error) {
	if err := r.enter(ctx); err != nil {
		return contract.CommitResult{}, err
	}
	defer r.leave()
	return r.attempts.CommitAcceptedOrder(ctx, request)
}

func (r *Runtime) FindOrderAttempt(ctx context.Context, commandID, proposalID string) (contract.OrderAttempt, error) {
	if err := r.enter(ctx); err != nil {
		return contract.OrderAttempt{}, err
	}
	defer r.leave()
	return r.attempts.FindOrderAttempt(ctx, commandID, proposalID)
}

func (r *Runtime) FindOrderAttemptByCommand(ctx context.Context, commandID string) (contract.OrderAttempt, error) {
	if err := r.enter(ctx); err != nil {
		return contract.OrderAttempt{}, err
	}
	defer r.leave()
	return r.attempts.FindOrderAttemptByCommand(ctx, commandID)
}

func (r *Runtime) ProcessDueExpirations(ctx context.Context) ([]contract.ExpiryResult, error) {
	if err := r.enter(ctx); err != nil {
		return nil, err
	}
	defer r.leave()
	return r.owner.ProcessDueExpirations(ctx)
}

func (r *Runtime) RunExpirations(ctx context.Context) (contract.ExpirationRun, error) {
	if err := r.enter(ctx); err != nil {
		return contract.ExpirationRun{}, err
	}
	defer r.leave()
	return r.owner.RunExpirations(ctx)
}

func (r *Runtime) ExpireBatch(ctx context.Context, request contract.ExpireBatchRequest) (contract.ExpiryResult, error) {
	if err := r.enter(ctx); err != nil {
		return contract.ExpiryResult{}, err
	}
	defer r.leave()
	var result contract.ExpiryResult
	err := r.attempts.RunOtherCommand(ctx, request.CommandID, func() error {
		var callErr error
		result, callErr = r.owner.ExpireBatch(ctx, request)
		return callErr
	})
	return result, err
}

func (r *Runtime) GetInventory(ctx context.Context) (contract.InventoryView, error) {
	if err := r.enter(ctx); err != nil {
		return contract.InventoryView{}, err
	}
	defer r.leave()
	return r.owner.GetInventory(ctx)
}

func (r *Runtime) GetExpiringBatches(ctx context.Context, cutoffAt time.Time) (contract.ExpiringBatches, error) {
	if err := r.enter(ctx); err != nil {
		return contract.ExpiringBatches{}, err
	}
	defer r.leave()
	return r.owner.GetExpiringBatches(ctx, cutoffAt)
}

func (r *Runtime) GetHistory(ctx context.Context) (contract.History, error) {
	if err := r.enter(ctx); err != nil {
		return contract.History{}, err
	}
	defer r.leave()
	return r.owner.GetHistory(ctx)
}

func (r *Runtime) SuggestID(ctx context.Context, kind contract.CatalogKind) (contract.IDSuggestion, error) {
	if err := r.enter(ctx); err != nil {
		return contract.IDSuggestion{}, err
	}
	defer r.leave()
	return r.owner.SuggestID(ctx, kind)
}

func (r *Runtime) GetProducts(ctx context.Context) (contract.Products, error) {
	if err := r.enter(ctx); err != nil {
		return contract.Products{}, err
	}
	defer r.leave()
	return r.owner.GetProducts(ctx)
}

func (r *Runtime) GetDishCatalog(ctx context.Context) (contract.DishCatalog, error) {
	if err := r.enter(ctx); err != nil {
		return contract.DishCatalog{}, err
	}
	defer r.leave()
	return r.owner.GetDishCatalog(ctx)
}

func (r *Runtime) GetDish(ctx context.Context, dishID string) (contract.DishDetails, error) {
	if err := r.enter(ctx); err != nil {
		return contract.DishDetails{}, err
	}
	defer r.leave()
	return r.owner.GetDish(ctx, dishID)
}

func (r *Runtime) FindRecordedCommand(ctx context.Context, commandID string) (contract.CommandRecord, bool, error) {
	if err := r.enter(ctx); err != nil {
		return contract.CommandRecord{}, false, err
	}
	defer r.leave()
	return r.findRecordedCommand(ctx, commandID)
}

func (r *Runtime) findRecordedCommand(ctx context.Context, commandID string) (contract.CommandRecord, bool, error) {
	record, found, err := r.owner.FindRecordedCommand(ctx, commandID)
	if err != nil || !found {
		return contract.CommandRecord{}, found, err
	}
	result := contract.CommandRecord{
		CommandID:          record.CommandID,
		CommandType:        record.CommandType,
		CommandFingerprint: record.CommandFingerprint,
		Result:             contract.CommandResult{Kind: contract.CommandResultKind(record.Result.Kind)},
	}
	if record.Result.Product != nil {
		product := contract.Product{ProductID: record.Result.Product.ProductID, Name: record.Result.Product.Name, BaseUnit: contract.BaseUnit(record.Result.Product.BaseUnit)}
		result.Result.Product = &product
	}
	if record.Result.Dish != nil {
		dish := contract.Dish{DishID: record.Result.Dish.DishID, Name: record.Result.Dish.Name, Ingredients: make([]contract.Ingredient, len(record.Result.Dish.Ingredients))}
		for index, ingredient := range record.Result.Dish.Ingredients {
			dish.Ingredients[index] = contract.Ingredient{ProductID: ingredient.ProductID, Quantity: contract.Quantity(ingredient.Quantity)}
		}
		result.Result.Dish = &dish
	}
	if record.Result.Delivery != nil {
		delivery := contract.DeliveryReceipt{DeliveryID: record.Result.Delivery.DeliveryID, BatchIDs: append([]string(nil), record.Result.Delivery.BatchIDs...)}
		result.Result.Delivery = &delivery
	}
	if record.Result.Order != nil {
		value := record.Result.Order
		order := contract.Order{
			OrderID:           value.OrderID,
			ProposalID:        value.ProposalID,
			AcceptedPositions: make([]contract.AcceptedPosition, len(value.AcceptedPositions)),
			Allocations:       make([]contract.Allocation, len(value.Allocations)),
			PlacedAt:          value.PlacedAt,
		}
		for index, position := range value.AcceptedPositions {
			order.AcceptedPositions[index] = contract.AcceptedPosition{LineNo: position.LineNo, DishID: position.DishID, Portions: position.Portions}
		}
		for index, allocation := range value.Allocations {
			order.Allocations[index] = contract.Allocation{ProductID: allocation.ProductID, BatchID: allocation.BatchID, Quantity: contract.Quantity(allocation.Quantity)}
		}
		result.Result.Order = &order
	}
	if record.Result.Pending != nil {
		v := record.Result.Pending
		result.Result.Pending = &contract.PendingResult{BatchID: v.BatchID, ProductID: v.ProductID, RemainingQuantity: contract.Quantity(v.RemainingQuantity), BaseUnit: contract.BaseUnit(v.BaseUnit), ExpiresAt: v.ExpiresAt}
	}
	if record.Result.Writeoff != nil {
		v := record.Result.Writeoff
		result.Result.Writeoff = &contract.WriteoffResult{BatchID: v.BatchID, ProductID: v.ProductID, WrittenOffQuantity: contract.Quantity(v.WrittenOffQuantity), BaseUnit: contract.BaseUnit(v.BaseUnit), ActorUserID: v.ActorUserID, ConfirmedAt: v.ConfirmedAt, EventID: v.EventID, OrganizationID: v.OrganizationID, RestaurantID: v.RestaurantID, RuntimeEpoch: v.RuntimeEpoch, TimeZone: v.TimeZone, SettingsVersion: v.SettingsVersion, StateVersion: v.StateVersion}
	}
	if record.Result.Expiry != nil {
		value := record.Result.Expiry
		expiry := contract.ExpiryResult{BatchID: value.BatchID, ProductID: value.ProductID, WrittenOffQuantity: contract.Quantity(value.WrittenOffQuantity), Reason: value.Reason, ExpiresAt: value.ExpiresAt, ProcessedAt: value.ProcessedAt}
		result.Result.Expiry = &expiry
	}
	return result, true, nil
}

func (r *Runtime) FindOrderByProposal(ctx context.Context, proposalID string) (contract.Order, bool, error) {
	if err := r.enter(ctx); err != nil {
		return contract.Order{}, false, err
	}
	defer r.leave()
	order, found, err := r.owner.FindOrderByProposal(ctx, proposalID)
	if err != nil || !found {
		return contract.Order{}, found, err
	}
	converted := contract.Order{
		OrderID:           order.OrderID,
		ProposalID:        order.ProposalID,
		AcceptedPositions: make([]contract.AcceptedPosition, len(order.AcceptedPositions)),
		Allocations:       make([]contract.Allocation, len(order.Allocations)),
		PlacedAt:          order.PlacedAt,
	}
	for index, position := range order.AcceptedPositions {
		converted.AcceptedPositions[index] = contract.AcceptedPosition{LineNo: position.LineNo, DishID: position.DishID, Portions: position.Portions}
	}
	for index, allocation := range order.Allocations {
		converted.Allocations[index] = contract.Allocation{ProductID: allocation.ProductID, BatchID: allocation.BatchID, Quantity: contract.Quantity(allocation.Quantity)}
	}
	return converted, true, nil
}

func (r *Runtime) RunSnapshotOnce(ctx context.Context) (bool, error) {
	if err := r.enter(ctx); err != nil {
		return false, err
	}
	defer r.leave()
	return r.owner.RunSnapshotOnce(ctx)
}

func (r *Runtime) RunSnapshotWorker(ctx context.Context) error {
	if err := r.enter(ctx); err != nil {
		return err
	}
	defer r.leave()
	worker, cancel := context.WithCancel(ctx)
	defer cancel()
	if r.gate != nil {
		stop := context.AfterFunc(r.gate.stop, cancel)
		defer stop()
	}
	err := r.owner.RunSnapshots(worker)
	if r.deleted() {
		return contract.ErrScopeDeleted
	}
	return err
}

func (r *Runtime) ConfirmWriteoff(ctx context.Context, request contract.ConfirmWriteoffRequest) (contract.WriteoffResult, error) {
	if err := r.enter(ctx); err != nil {
		return contract.WriteoffResult{}, err
	}
	defer r.leave()
	var result contract.WriteoffResult
	err := r.attempts.RunOtherCommand(ctx, request.CommandID, func() error { var err error; result, err = r.owner.ConfirmWriteoff(ctx, request); return err })
	return result, err
}
func (r *Runtime) ResolveWriteoff(ctx context.Context, commandID, batchID string) (contract.WriteoffObservation, error) {
	if err := r.enter(ctx); err != nil {
		return contract.WriteoffObservation{}, err
	}
	defer r.leave()
	return r.owner.ResolveWriteoff(ctx, commandID, batchID)
}

func (r *Runtime) PrepareStock(ctx context.Context) (contract.StockPreparation, error) {
	if err := r.enter(ctx); err != nil {
		return contract.StockPreparation{}, err
	}
	defer r.leave()
	return r.owner.PrepareStock(ctx)
}
func (r *Runtime) CalculateStock(ctx context.Context, prepared contract.StockPreparation) (contract.InventoryView, error) {
	if err := r.enter(ctx); err != nil {
		return contract.InventoryView{}, err
	}
	defer r.leave()
	return application.CalculateStock(ctx, prepared)
}
