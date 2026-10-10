package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestDueAtBoundary(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	owner, store, _ := newSeededOwner(t, dueAt, 10000, 6000)
	state, err := owner.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.State.Batches["batch"].RemainingQuantity; got != 0 {
		t.Fatalf("remaining at dueAt boundary = %d; want 0", got)
	}
	if state.StateVersion != 5 {
		t.Fatalf("state version = %d; want 5 including BatchExpired", state.StateVersion)
	}
	command, ok, err := store.FindCommand(context.Background(), "expiration/batch")
	if err != nil || !ok || command.Result.Expiry == nil || command.Result.Expiry.WrittenOffQuantity != 4000 {
		t.Fatalf("recorded expiration = %#v, %v, %v; want 4000 after the 6000 order", command, ok, err)
	}
}

func TestMissedTick(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	owner, _, _ := newSeededOwner(t, dueAt.Add(48*time.Hour), 10000, 0)
	results, err := owner.ProcessDueExpirations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].WrittenOffQuantity != 10000 {
		t.Fatalf("ProcessDueExpirations() = %#v; want one full write-off after skipped ticks", results)
	}
}

func TestDueDuringPreparation(t *testing.T) {
	firstDue := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, firstDue, 10000, 0)
	appendSecondBatch(t, store, firstDue.Add(30*time.Minute), 7000)
	clock := &sequenceClock{times: []time.Time{firstDue, firstDue.Add(time.Hour), firstDue.Add(time.Hour)}}
	owner, err := NewOwner(store, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	state, err := owner.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.State.Batches["batch"].RemainingQuantity != 0 || state.State.Batches["batch-2"].RemainingQuantity != 0 {
		t.Fatalf("prepared balances = (%d, %d); want both due batches expired", state.State.Batches["batch"].RemainingQuantity, state.State.Batches["batch-2"].RemainingQuantity)
	}
	if state.StateVersion != 6 {
		t.Fatalf("state version = %d; want 6 after both expiration commands", state.StateVersion)
	}
}

func TestPartialExpirationFailure(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	appendSecondBatch(t, base, dueAt, 7000)
	wrapped := &eventStoreWrapper{EventStore: base, append: func(batch domain.CommandBatch, result domain.AppendResult, err error) (domain.AppendResult, error) {
		if batch.CommandID == "expiration/batch-2" {
			return domain.AppendResult{}, errors.New("second batch write failed")
		}
		return result, err
	}}
	clock := &testClock{now: dueAt}
	owner, err := NewOwner(wrapped, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Prepare(context.Background())
	if !errors.Is(err, contract.ErrInventoryUnavailable) {
		t.Fatalf("Prepare() error = %v; want InventoryUnavailable", err)
	}
	if got := owner.state.Batches["batch"].RemainingQuantity; got != 0 {
		t.Fatalf("first committed balance = %d; want 0 retained after second failure", got)
	}
	if got := owner.state.Batches["batch-2"].RemainingQuantity; got != 7000 {
		t.Fatalf("second batch balance = %d; want 7000", got)
	}
	if got, exists := owner.expirations["batch-2"]; !exists || !got.Equal(dueAt) {
		t.Fatalf("second task dueAt = %v, exists %v; want retained task due at %v", got, exists, dueAt)
	}
	first, ok, err := base.FindCommand(context.Background(), "expiration/batch")
	if err != nil || !ok || first.Result.Expiry == nil {
		t.Fatalf("first expiration = %#v, %v, %v; want its committed result", first, ok, err)
	}
}

func appendSecondBatch(t *testing.T, store *memory.EventStore, expiresAt time.Time, quantity domain.Quantity) {
	t.Helper()
	at := moscowTime(2026, time.April, 1, 10)
	delivery := domain.Delivery{DeliveryID: "delivery-2", Number: "invoice-2", CreatedAt: at, Lines: []domain.DeliveryLine{{ProductID: "p", ManufacturedAt: at, ShelfLife: domain.ShelfLife{Value: 1, Unit: domain.ShelfLifeDay}, ReceivedAt: at, Quantity: quantity}}}
	batch := domain.Batch{BatchID: "batch-2", DeliveryID: "delivery-2", LineNo: 1, ProductID: "p", ManufacturedAt: at, ShelfLife: domain.ShelfLife{Value: 1, Unit: domain.ShelfLifeDay}, ReceivedAt: at, ExpiresAt: expiresAt, ReceivedQuantity: quantity, RemainingQuantity: quantity}
	appendFixture(t, context.Background(), store, "receive-2", "ReceiveDelivery", domain.Event{EventID: "event-delivery-2", RecordedAt: at, Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{Delivery: &delivery, Batches: []domain.Batch{batch}}})
}

type sequenceClock struct {
	times []time.Time
	index int
}

func (c *sequenceClock) Now() time.Time {
	if c.index >= len(c.times) {
		return c.times[len(c.times)-1]
	}
	now := c.times[c.index]
	c.index++
	return now
}

func newSeededOwner(t *testing.T, now time.Time, quantity, consumed domain.Quantity) (*Owner, *memory.EventStore, *testClock) {
	t.Helper()
	store := seedStore(t, now, quantity, consumed)
	clock := &testClock{now: now}
	owner, err := NewOwner(store, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	return owner, store, clock
}

func nextTestID() func() string {
	var n int
	return func() string {
		n++
		return fmt.Sprintf("owner-event-%d", n)
	}
}
