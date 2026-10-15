package application

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestFINPeriodUsesRecordedAtAndOnlyActualConsumption(t *testing.T) {
	at := moscowTime(2026, time.October, 15, 12)
	from := at.Add(-168 * time.Hour)
	order := func(recorded time.Time, quantity domain.Quantity) domain.Event {
		return domain.Event{RecordedAt: recorded, Type: domain.EventOrderPlaced, Payload: domain.EventPayload{Order: &domain.Order{PlacedAt: at.Add(-900 * time.Hour), AcceptedPositions: []domain.AcceptedPosition{{DishID: "d", Portions: 2}}, Allocations: []domain.Allocation{{ProductID: "p", Quantity: quantity}}}}}
	}
	batches := []domain.RecordedBatch{{Events: []domain.Event{
		order(from.Add(-time.Nanosecond), 100), order(from, 200), order(at.Add(-time.Nanosecond), 300), order(at, 400),
		{RecordedAt: from, Type: domain.EventBatchExpiryPending, Payload: domain.EventPayload{Pending: &domain.PendingResult{ProductID: "p", RemainingQuantity: 700}}},
		{RecordedAt: from, Type: domain.EventBatchExpired, Payload: domain.EventPayload{Expiry: &domain.ExpiryResult{ProductID: "p", WrittenOffQuantity: 700}}},
		{RecordedAt: from, Type: domain.EventBatchWriteoffConfirmed, Payload: domain.EventPayload{Writeoff: &domain.WriteoffResult{ProductID: "p", WrittenOffQuantity: 600}}},
	}}}
	facts, err := planningPeriod(context.Background(), batches, from, at)
	if err != nil || facts.consumed["p"] != 1100 || facts.demand["d"] != 4 {
		t.Fatalf("RecordedAt/count: %+v %v", facts, err)
	}
	batches[0].Events = []domain.Event{order(from, math.MaxInt64), order(from, 1)}
	if _, err := planningPeriod(context.Background(), batches, from, at); !errors.Is(err, contract.ErrCalculationUnavailable) {
		t.Fatalf("period sum overflow: %v", err)
	}
	batches[0].Events = []domain.Event{order(from, 1), order(from, 1)}
	batches[0].Events[0].Payload.Order.AcceptedPositions[0].Portions = math.MaxInt64
	if _, err := planningPeriod(context.Background(), batches, from, at); !errors.Is(err, contract.ErrCalculationUnavailable) {
		t.Fatalf("portion sum overflow: %v", err)
	}
}

func TestFINPlanningCaptureIsReadOnlyAndRejectsStaleScope(t *testing.T) {
	at := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, at, 10000, 6000)
	clock := &testClock{now: at.Add(-time.Nanosecond)}
	owner := w3Owner(t, store, clock)
	ctx := context.Background()
	if err := owner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	planning, err := NewPlanning(owner, at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(at)
	request := contract.PlanningRequest{Scope: w3Scope(), Actor: w3Actor}
	before, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	supply, err := planning.Supply(ctx, request)
	if err != nil || len(supply.Items) != 1 || supply.Items[0].AvailableQuantity != 0 || supply.Items[0].ConsumedQuantity != 6000 || supply.Items[0].SuggestedQuantity != 6000 || supply.Items[0].AverageDailyBaseUnits != "0.857143" || supply.Coverage.Complete || supply.Identity.StateVersion != before {
		t.Fatalf("supply %+v %v", supply, err)
	}
	inputs, err := planning.Recommendations(ctx, request)
	if err != nil || len(inputs.Menu.Dishes) != 1 || inputs.Menu.Dishes[0].IsAvailable || len(inputs.RiskBatches) != 0 || len(inputs.Demand) != 1 || inputs.Demand[0].Portions != 1 || !inputs.Menu.EvaluatedAt.Equal(at) || inputs.Menu.StateVersion != before {
		t.Fatalf("inputs %+v %v", inputs, err)
	}
	supply.Items[0].BlockedDishIDs[0] = "changed"
	again, err := planning.Supply(ctx, request)
	if err != nil || again.Items[0].BlockedDishIDs[0] != "d" {
		t.Fatalf("caller alias: %+v %v", again, err)
	}
	after, err := store.Version(ctx)
	if err != nil || before != after || owner.state.Batches["batch"].PendingWriteoff {
		t.Fatalf("read appended due expiry: %d -> %d %v", before, after, err)
	}
	request.Scope.RuntimeEpoch = "88888888-8888-4888-8888-888888888888"
	if _, err := planning.Supply(ctx, request); !errors.Is(err, contract.ErrRuntimeReset) {
		t.Fatalf("epoch refusal: %v", err)
	}
	request.Scope = w3Scope()
	request.Scope.OrganizationID = "88888888-8888-4888-8888-888888888888"
	if _, err := planning.Recommendations(ctx, request); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("scope refusal: %v", err)
	}
}
