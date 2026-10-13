package protocol

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
)

func TestW3RealPreparationSurvivesCalculationFault(t *testing.T) {
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.FixedZone("MSK", 10800))
	clock := at
	scope := contract.Scope{OrganizationID: "22222222-2222-4222-8222-222222222222", RestaurantID: "33333333-3333-4333-8333-333333333333", RuntimeEpoch: "44444444-4444-4444-8444-444444444444", LifecycleVersion: "1", TimeZone: "Europe/Moscow", SettingsVersion: "1"}
	runtime, err := bootstrap.NewInMemory(bootstrap.Options{Scope: scope, Now: func() time.Time { return clock }, NewID: rand.Text, EventsPerSnapshot: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := runtime.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p", ProductID: "P", Name: "Product", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "d", DeliveryID: "d", Number: "1", CreatedAt: at, Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 100, ManufacturedAt: at, ReceivedAt: at, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}}); err != nil {
		t.Fatal(err)
	}
	clock = at.Add(24 * time.Hour)
	fault, err := NewAnalyticsFault(func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	id := "77777777-7777-4777-8777-777777777777"
	if _, err := fault.Arm(FaultArm{id, AnalyticsPoint, "inventory", "read-1", 30000, 1}); err != nil {
		t.Fatal(err)
	}
	prepared, err := runtime.PrepareStock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.InvalidationRequired || len(prepared.Batches) != 1 || !prepared.Batches[0].PendingWriteoff {
		t.Fatalf("real preparation=%+v", prepared)
	}
	if _, err := fault.Calculate(ctx, "inventory", "read-1", prepared, runtime.CalculateStock); !errors.Is(err, ErrAnalyticsFault) {
		t.Fatalf("fault=%v", err)
	}
	state, err := fault.Read(id)
	if err != nil || state.Hits != 1 || state.OwnerOutcome != "committed" {
		t.Fatalf("observation=%+v %v", state, err)
	}
	inventory, err := fault.Calculate(ctx, "inventory", "read-1", prepared, runtime.CalculateStock)
	if err != nil || inventory.Products[0].Quantity != 100 {
		t.Fatalf("one-shot calculation=%+v %v", inventory, err)
	}
	history, err := runtime.GetHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if history.Events[len(history.Events)-1].Type != "BatchExpiryPending" {
		t.Fatal("fault erased real fact")
	}
}
func TestW3AnalyticsFaultBoundsAndConcurrency(t *testing.T) {
	at := time.Now()
	fault, err := NewAnalyticsFault(func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	id := "77777777-7777-4777-8777-777777777777"
	input := FaultArm{id, AnalyticsPoint, "inventory", "read", 30000, 1}
	if _, err := fault.Arm(input); err != nil {
		t.Fatal(err)
	}
	if _, err := fault.Arm(input); !errors.Is(err, ErrFaultConflict) {
		t.Fatal(err)
	}
	var mu sync.Mutex
	hits := 0
	var wg sync.WaitGroup
	calc := func(context.Context, contract.StockPreparation) (contract.InventoryView, error) {
		return contract.InventoryView{}, nil
	}
	if _, err := fault.Calculate(context.Background(), "inventory", "other", contract.StockPreparation{}, calc); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		wg.Go(func() {
			_, err := fault.Calculate(context.Background(), "inventory", "read", contract.StockPreparation{}, calc)
			if errors.Is(err, ErrAnalyticsFault) {
				mu.Lock()
				hits++
				mu.Unlock()
			} else if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if hits != 1 {
		t.Fatalf("hits=%d", hits)
	}
	if _, err := fault.Clear(id); err != nil {
		t.Fatal(err)
	}
	input.ExpireAfterMs = 1
	if _, err := fault.Arm(input); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Millisecond)
	state, err := fault.Read(id)
	if err != nil || state.State != "expired" {
		t.Fatalf("expiry=%+v %v", state, err)
	}
	input.Count = 2
	if _, err := fault.Arm(input); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
	input.Count = 1
	input.Point = "not-supported"
	if _, err := fault.Arm(input); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatal(err)
	}
}
