package application

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestApplyBatchLockedKeepsExpirationIndexInStepWithState(t *testing.T) {
	ctx := context.Background()
	expiry := moscowTime(2026, time.April, 2, 10)
	at := moscowTime(2026, time.April, 1, 10)
	store := seedStore(t, expiry, 10000, 6000)
	owner, err := NewOwner(store, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	owner.mu.lockUncancellable()
	defer owner.mu.Unlock()
	apply := func(commandID, commandType string, events ...domain.Event) error {
		return owner.applyBatchLocked(asRecordedBatch(domain.CommandBatch{ExpectedVersion: owner.state.StateVersion, CommandID: commandID, CommandType: commandType, CommandFingerprint: "fp-" + commandID, Events: events}))
	}
	assertIndex := func(step string, want ...string) {
		t.Helper()
		rebuilt, err := expirationIndex(owner.state)
		if err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(rebuilt, owner.expirations) {
			t.Fatalf("%s: incremental index %v differs from the index rebuilt from state %v", step, owner.expirations, rebuilt)
		}
		if len(owner.expirations) != len(want) {
			t.Fatalf("%s: index %v; want batches %v", step, owner.expirations, want)
		}
		for _, id := range want {
			if _, ok := owner.expirations[id]; !ok {
				t.Fatalf("%s: index %v; want batches %v", step, owner.expirations, want)
			}
		}
	}
	assertIndex("recovered", "batch")

	delivery := domain.Delivery{DeliveryID: "delivery-2", Number: "invoice-2", CreatedAt: at, Lines: []domain.DeliveryLine{{ProductID: "p", ManufacturedAt: at, ShelfLife: domain.ShelfLife{Value: 1, Unit: domain.ShelfLifeDay}, ReceivedAt: at, Quantity: 8000}}}
	second := domain.Batch{BatchID: "batch-2", DeliveryID: "delivery-2", LineNo: 1, ProductID: "p", ManufacturedAt: at, ShelfLife: domain.ShelfLife{Value: 1, Unit: domain.ShelfLifeDay}, ReceivedAt: at, ExpiresAt: expiry.Add(24 * time.Hour), ReceivedQuantity: 8000, RemainingQuantity: 8000}
	if err := apply("receive-2", "ReceiveDelivery", domain.Event{EventID: "event-delivery-2", RecordedAt: at, Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{Delivery: &delivery, Batches: []domain.Batch{second}}}); err != nil {
		t.Fatal(err)
	}
	assertIndex("delivery adds a batch", "batch", "batch-2")

	rejected := domain.Order{OrderID: "order-bad", ProposalID: "proposal-bad", AcceptedPositions: []domain.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}, Allocations: []domain.Allocation{{ProductID: "p", BatchID: "missing", Quantity: 6000}}, PlacedAt: at}
	before, version := domain.CloneInventoryState(owner.state), owner.state.StateVersion
	if err := apply("order-bad", "ConfirmOrder", domain.Event{EventID: "event-order-bad", RecordedAt: at, Type: domain.EventOrderPlaced, Payload: domain.EventPayload{Order: &rejected}}); !errors.Is(err, domain.ErrBatchNotFound) {
		t.Fatalf("rejected order error = %v; want ErrBatchNotFound", err)
	}
	if !domain.EqualInventoryState(before, owner.state) || owner.state.StateVersion != version {
		t.Fatal("a rejected batch changed the state")
	}
	assertIndex("rejected batch", "batch", "batch-2")

	order := domain.Order{OrderID: "order-2", ProposalID: "proposal-2", AcceptedPositions: []domain.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}, Allocations: []domain.Allocation{{ProductID: "p", BatchID: "batch", Quantity: 4000}, {ProductID: "p", BatchID: "batch-2", Quantity: 2000}}, PlacedAt: at}
	if err := apply("order-2", "ConfirmOrder", domain.Event{EventID: "event-order-2", RecordedAt: at, Type: domain.EventOrderPlaced, Payload: domain.EventPayload{Order: &order}}); err != nil {
		t.Fatal(err)
	}
	assertIndex("order exhausts a batch", "batch-2")
}
