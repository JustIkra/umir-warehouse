package application

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type planningSpyStore struct {
	EventStore
	reads, writes int
}

func TestFINSupplyCapacityRefusesPartialResult(t *testing.T) {
	at := moscowTime(2026, time.April, 1, 10)
	owner, _, _ := newCatalogOwner(t, at)
	if err := owner.ConfigureScope(w3Scope()); err != nil {
		t.Fatal(err)
	}
	planning, err := NewPlanning(owner, at)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := range 501 {
		id := strconv.Itoa(i)
		if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p" + id, ProductID: id, Name: id, BaseUnit: contract.BaseUnitG}); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "d" + id, DishID: id, Name: id, Ingredients: []contract.Ingredient{{ProductID: id, Quantity: 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := planning.Supply(ctx, contract.PlanningRequest{Scope: w3Scope(), Actor: w3Actor})
	if !errors.Is(err, contract.ErrLimitExceeded) || len(result.Items) != 0 {
		t.Fatalf("truncated successful suggestions: %d rows, %v", len(result.Items), err)
	}
}

func (s *planningSpyStore) ReadPeriod(ctx context.Context, query PeriodQuery) ([]domain.Event, error) {
	s.reads++
	return s.EventStore.ReadPeriod(ctx, query)
}

func (s *planningSpyStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	s.writes++
	return s.EventStore.AppendBatch(ctx, batch)
}

func TestFINPlanningRiskHorizonAndWholeRecipeCapture(t *testing.T) {
	at := moscowTime(2026, time.April, 1, 10)
	store := seedStore(t, at.Add(72*time.Hour), 1000, 0)
	delivery := domain.Delivery{DeliveryID: "fresh", Number: "fresh", CreatedAt: at, Lines: []domain.DeliveryLine{{ProductID: "p", ManufacturedAt: at, ReceivedAt: at, Quantity: 10000, ShelfLife: domain.ShelfLife{Value: 4, Unit: domain.ShelfLifeDay}}}}
	batch := domain.Batch{BatchID: "fresh", DeliveryID: "fresh", LineNo: 1, ProductID: "p", ManufacturedAt: at, ReceivedAt: at, ExpiresAt: at.Add(72*time.Hour + time.Nanosecond), ReceivedQuantity: 10000, RemainingQuantity: 10000, ShelfLife: domain.ShelfLife{Value: 4, Unit: domain.ShelfLifeDay}}
	appendFixture(t, context.Background(), store, "fresh", "ReceiveDelivery", domain.Event{EventID: "fresh-event", RecordedAt: at, Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{Delivery: &delivery, Batches: []domain.Batch{batch}}})
	spy := &planningSpyStore{EventStore: store}
	owner := w3Owner(t, spy, &testClock{now: at})
	if err := owner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	planning, err := NewPlanning(owner, at.Add(-168*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	spy.reads, spy.writes = 0, 0
	request := contract.PlanningRequest{Scope: w3Scope(), Actor: w3Actor}
	inputs, err := planning.Recommendations(context.Background(), request)
	if err != nil || len(inputs.RiskBatches) != 1 || inputs.RiskBatches[0].BatchID != "batch" || inputs.RiskBatches[0].RemainingSeconds != 72*3600 || !inputs.Coverage.Complete || !inputs.Menu.Dishes[0].IsAvailable || len(inputs.Menu.Dishes[0].Allocations) != 2 {
		t.Fatalf("risk/capture %+v %v", inputs, err)
	}
	quantity := contract.Quantity(0)
	for _, allocation := range inputs.Menu.Dishes[0].Allocations {
		quantity += allocation.Quantity
	}
	if quantity != 6000 || spy.reads != 1 || spy.writes != 0 {
		t.Fatalf("whole recipe=%d, journal scans=%d, writes=%d", quantity, spy.reads, spy.writes)
	}
	inputs.Menu.Dishes[0].Allocations[0].Quantity = 1
	again, err := planning.Recommendations(context.Background(), request)
	if err != nil || again.Menu.Dishes[0].Allocations[0].Quantity != 1000 {
		t.Fatalf("menu capture alias: %+v %v", again, err)
	}
	owner.pending = &domain.CommandBatch{CommandID: "unresolved"}
	spy.reads, spy.writes = 0, 0
	if _, err := planning.Supply(context.Background(), request); !errors.Is(err, contract.ErrInventoryUnavailable) || spy.reads != 0 || spy.writes != 0 {
		t.Fatalf("read resolved pending mutation: %v, scans=%d, writes=%d", err, spy.reads, spy.writes)
	}
	owner.pending = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := planning.Recommendations(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}
