package application

import (
	"context"
	"errors"
	"math"
	"math/big"
	"sort"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

const planningHours = 168
const recommendationHorizonHours = 72

type Planning struct {
	owner    *Owner
	coverage time.Time
}

type periodFacts struct {
	consumed map[string]domain.Quantity
	demand   map[string]int64
}

// planningCapture is what a planning request fixes under the owner's lock:
// the report identity, a private shallow view of the inventory and the period
// whose facts are read afterwards.
type planningCapture struct {
	identity contract.ReportIdentity
	window   contract.PlanningWindow
	coverage contract.Coverage
	view     domain.InventoryState
	facts    periodFacts
}

func NewPlanning(owner *Owner, coverage time.Time) (*Planning, error) {
	if owner == nil || coverage.IsZero() {
		return nil, contract.ErrInvalidArgument
	}
	return &Planning{owner: owner, coverage: domain.CanonicalBusinessTime(coverage)}, nil
}

// planningPeriod sums what the events of [from, to) consumed and what was
// asked for. The store already selects the period; the check keeps the sums
// right for any source.
func planningPeriod(ctx context.Context, events []domain.Event, from, to time.Time) (periodFacts, error) {
	facts := periodFacts{consumed: map[string]domain.Quantity{}, demand: map[string]int64{}}
	add := func(id string, quantity domain.Quantity) error {
		if id == "" || quantity <= 0 {
			return contract.ErrCalculationUnavailable
		}
		total, err := domain.AddQuantity(facts.consumed[id], quantity)
		if err != nil {
			return errors.Join(contract.ErrCalculationUnavailable, err)
		}
		facts.consumed[id] = total
		return nil
	}
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return periodFacts{}, err
		}
		if event.RecordedAt.Before(from) || !event.RecordedAt.Before(to) {
			continue
		}
		switch event.Type {
		case domain.EventOrderPlaced:
			order := event.Payload.Order
			if order == nil {
				return periodFacts{}, contract.ErrCalculationUnavailable
			}
			for _, allocation := range order.Allocations {
				if err := add(allocation.ProductID, allocation.Quantity); err != nil {
					return periodFacts{}, err
				}
			}
			for _, position := range order.AcceptedPositions {
				if position.DishID == "" || position.Portions <= 0 || facts.demand[position.DishID] > math.MaxInt64-position.Portions {
					return periodFacts{}, contract.ErrCalculationUnavailable
				}
				facts.demand[position.DishID] += position.Portions
			}
		case domain.EventBatchWriteoffConfirmed:
			if event.Payload.Writeoff == nil {
				return periodFacts{}, contract.ErrCalculationUnavailable
			}
			value := event.Payload.Writeoff
			if err := add(value.ProductID, value.WrittenOffQuantity); err != nil {
				return periodFacts{}, err
			}
		}
	}
	return facts, nil
}

// capture fixes a planning request: under the owner's lock it checks the scope
// and takes the version, the clock and a shallow copy of the inventory; the
// facts of the period up to that version are then read and summed without it.
func (p *Planning) capture(ctx context.Context, request contract.PlanningRequest) (planningCapture, error) {
	p.owner.mu.Lock()
	capture, err := p.captureLocked(ctx, request)
	p.owner.mu.Unlock()
	if err != nil {
		return planningCapture{}, err
	}
	events, err := readPeriod(ctx, p.owner.events, PeriodQuery{
		From: capture.window.From, To: capture.window.To,
		Types:        []domain.EventType{domain.EventOrderPlaced, domain.EventBatchWriteoffConfirmed},
		UpToSequence: capture.identity.StateVersion,
	})
	if err != nil {
		return planningCapture{}, inventoryUnavailable(err)
	}
	if err := checkEventSchemas(events); err != nil {
		return planningCapture{}, inventoryUnavailable(err)
	}
	capture.facts, err = planningPeriod(ctx, events, capture.window.From, capture.window.To)
	if err != nil {
		return planningCapture{}, err
	}
	return capture, nil
}

func (p *Planning) captureLocked(ctx context.Context, request contract.PlanningRequest) (planningCapture, error) {
	if err := ctx.Err(); err != nil {
		return planningCapture{}, err
	}
	o := p.owner
	scope := request.Scope
	if scope.Validate() != nil || !contract.ValidEntityID(request.Actor) {
		return planningCapture{}, contract.ErrInvalidArgument
	}
	if scope.OrganizationID != o.scope.OrganizationID || scope.RestaurantID != o.scope.RestaurantID || scope.LifecycleVersion != o.scope.LifecycleVersion {
		return planningCapture{}, contract.ErrForbidden
	}
	if scope.RuntimeEpoch != o.scope.RuntimeEpoch {
		return planningCapture{}, contract.ErrRuntimeReset
	}
	if _, err := o.loadZone(scope.TimeZone); err != nil {
		return planningCapture{}, calendarError(err)
	}
	if !o.usable || o.pending != nil {
		return planningCapture{}, contract.ErrInventoryUnavailable
	}
	version, err := o.events.Version(ctx)
	if err != nil || version != o.state.StateVersion {
		return planningCapture{}, errors.Join(contract.ErrInventoryUnavailable, err)
	}
	at := domain.CanonicalBusinessTime(o.now())
	if p.coverage.After(at) {
		return planningCapture{}, contract.ErrCalculationUnavailable
	}
	from := at.Add(-planningHours * time.Hour)
	return planningCapture{
		identity: contract.ReportIdentity{Scope: scope, EvaluatedAt: at, StateVersion: version, PrefixSequence: version},
		window:   contract.PlanningWindow{From: from, To: at, Days: domain.SupplyWindowDays},
		coverage: contract.Coverage{Epoch: scope.RuntimeEpoch, CoverageFrom: p.coverage, Complete: !from.Before(p.coverage), RequestedFrom: from, AvailableFrom: maxTime(from, p.coverage)},
		view:     domain.CloneReadView(o.state),
	}, nil
}

func (p *Planning) Supply(ctx context.Context, request contract.PlanningRequest) (contract.SupplySuggestions, error) {
	capture, err := p.capture(ctx, request)
	if err != nil {
		return contract.SupplySuggestions{}, err
	}
	rows, err := domain.CalculateSupply(capture.view, capture.facts.consumed, capture.identity.EvaluatedAt)
	if err != nil {
		return contract.SupplySuggestions{}, errors.Join(contract.ErrCalculationUnavailable, err)
	}
	if len(rows) > 500 {
		return contract.SupplySuggestions{}, contract.ErrLimitExceeded
	}
	result := contract.SupplySuggestions{Identity: capture.identity, Policy: contract.SupplyPolicy{Version: "FIN1", HistoryDays: domain.SupplyWindowDays, CoverageThresholdDays: domain.SupplyThresholdDays, TargetDays: domain.SupplyTargetDays}, Window: capture.window, Coverage: capture.coverage, Items: make([]contract.SupplyRow, 0, len(rows))}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return contract.SupplySuggestions{}, err
		}
		if len(row.BlockedDishIDs) > 500 {
			return contract.SupplySuggestions{}, contract.ErrLimitExceeded
		}
		reasons := make([]string, 0, 2)
		if row.LowCoverage {
			reasons = append(reasons, "low_coverage")
		}
		if row.OnePortionDeficit > 0 {
			reasons = append(reasons, "stop_list_deficit")
		}
		var coverage *string
		if row.ConsumedQuantity > 0 {
			value := new(big.Rat).SetFrac(new(big.Int).Mul(big.NewInt(int64(row.AvailableQuantity)), big.NewInt(domain.SupplyWindowDays)), big.NewInt(int64(row.ConsumedQuantity))).FloatString(6)
			coverage = &value
		}
		average := new(big.Rat).SetFrac(big.NewInt(int64(row.ConsumedQuantity)), big.NewInt(domain.SupplyWindowDays*1000)).FloatString(6)
		result.Items = append(result.Items, contract.SupplyRow{ProductID: row.Product.ProductID, ProductName: row.Product.Name, BaseUnit: contract.BaseUnit(row.Product.BaseUnit), AvailableQuantity: contract.Quantity(row.AvailableQuantity), ConsumedQuantity: contract.Quantity(row.ConsumedQuantity), OnePortionDeficit: contract.Quantity(row.OnePortionDeficit), SuggestedQuantity: contract.Quantity(row.SuggestedQuantity), AverageDailyBaseUnits: average, CoverageDays: coverage, BlockedDishIDs: row.BlockedDishIDs, Reasons: reasons})
	}
	return result, nil
}

func (p *Planning) Recommendations(ctx context.Context, request contract.PlanningRequest) (contract.RecommendationInputs, error) {
	capture, err := p.capture(ctx, request)
	if err != nil {
		return contract.RecommendationInputs{}, err
	}
	state := capture.view
	view := domain.InventoryState{Products: state.Products, Dishes: state.Dishes, Batches: make(map[string]domain.Batch), StateVersion: state.StateVersion}
	result := contract.RecommendationInputs{Identity: capture.identity, HorizonHours: recommendationHorizonHours, Window: capture.window, Coverage: capture.coverage, RiskBatches: []contract.RiskBatch{}, Demand: []contract.DishDemand{}}
	cutoff := capture.identity.EvaluatedAt.Add(recommendationHorizonHours * time.Hour)
	for id, batch := range state.Batches {
		if err := ctx.Err(); err != nil {
			return contract.RecommendationInputs{}, err
		}
		if !domain.EligibleBatch(batch, capture.identity.EvaluatedAt) {
			continue
		}
		view.Batches[id] = batch
		if batch.ExpiresAt.After(cutoff) {
			continue
		}
		product, exists := state.Products[batch.ProductID]
		if !exists {
			return contract.RecommendationInputs{}, contract.ErrCalculationUnavailable
		}
		result.RiskBatches = append(result.RiskBatches, contract.RiskBatch{ProductID: product.ProductID, ProductName: product.Name, BatchID: batch.BatchID, RemainingQuantity: contract.Quantity(batch.RemainingQuantity), BaseUnit: contract.BaseUnit(product.BaseUnit), ExpiresAt: batch.ExpiresAt, RemainingSeconds: int64(batch.ExpiresAt.Sub(capture.identity.EvaluatedAt) / time.Second)})
	}
	if len(result.RiskBatches) > 10000 || len(state.Dishes) > 10000 || len(capture.facts.demand) > 10000 {
		return contract.RecommendationInputs{}, contract.ErrLimitExceeded
	}
	menu, err := evaluateMenu(ctx, ReadState{State: view, EvaluatedAt: capture.identity.EvaluatedAt, StateVersion: capture.identity.StateVersion})
	if err != nil {
		return contract.RecommendationInputs{}, errors.Join(contract.ErrCalculationUnavailable, err)
	}
	result.Menu = menu
	for id, portions := range capture.facts.demand {
		result.Demand = append(result.Demand, contract.DishDemand{DishID: id, Portions: portions})
	}
	sort.Slice(result.RiskBatches, func(i, j int) bool {
		a, b := result.RiskBatches[i], result.RiskBatches[j]
		if a.ProductID != b.ProductID {
			return a.ProductID < b.ProductID
		}
		if !a.ExpiresAt.Equal(b.ExpiresAt) {
			return a.ExpiresAt.Before(b.ExpiresAt)
		}
		return a.BatchID < b.BatchID
	})
	sort.Slice(result.Demand, func(i, j int) bool { return result.Demand[i].DishID < result.Demand[j].DishID })
	return result, nil
}
