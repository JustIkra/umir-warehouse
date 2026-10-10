package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type testClock struct {
	now time.Time
}

func (c *testClock) Now() time.Time {
	return c.now
}

func (c *testClock) Set(now time.Time) {
	c.now = now
}

type eventStoreWrapper struct {
	EventStore
	append func(domain.CommandBatch, domain.AppendResult, error) (domain.AppendResult, error)
	read   func(int64, []domain.RecordedBatch, error) ([]domain.RecordedBatch, error)
}

func (s *eventStoreWrapper) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	result, err := s.EventStore.AppendBatch(ctx, batch)
	if s.append != nil {
		return s.append(batch, result, err)
	}
	return result, err
}

func (s *eventStoreWrapper) ReadBatches(ctx context.Context, afterSequence int64) ([]domain.RecordedBatch, error) {
	batches, err := s.EventStore.ReadBatches(ctx, afterSequence)
	if s.read != nil {
		return s.read(afterSequence, batches, err)
	}
	return batches, err
}

func seedStore(t *testing.T, expiresAt time.Time, quantity domain.Quantity, consumed domain.Quantity) *memory.EventStore {
	t.Helper()
	store := memory.NewEventStore()
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	appendFixture(t, ctx, store, "register-p", "RegisterProduct", domain.Event{EventID: "event-register-p", RecordedAt: at, Type: domain.EventProductRegistered, Payload: domain.EventPayload{Product: &domain.Product{ProductID: "p", Name: "Product", BaseUnit: domain.BaseUnitG}}})
	dishQuantity := consumed
	if dishQuantity == 0 {
		dishQuantity = 6000
	}
	appendFixture(t, ctx, store, "define-d", "DefineDish", domain.Event{EventID: "event-define-d", RecordedAt: at, Type: domain.EventDishDefined, Payload: domain.EventPayload{Dish: &domain.Dish{DishID: "d", Name: "Dish", Ingredients: []domain.Ingredient{{ProductID: "p", Quantity: dishQuantity}}}}})
	delivery := domain.Delivery{DeliveryID: "delivery", Number: "invoice", CreatedAt: at, Lines: []domain.DeliveryLine{{ProductID: "p", ManufacturedAt: at, ShelfLife: domain.ShelfLife{Value: 1, Unit: domain.ShelfLifeDay}, ReceivedAt: at, Quantity: quantity}}}
	batch := domain.Batch{BatchID: "batch", DeliveryID: "delivery", LineNo: 1, ProductID: "p", ManufacturedAt: at, ShelfLife: domain.ShelfLife{Value: 1, Unit: domain.ShelfLifeDay}, ReceivedAt: at, ExpiresAt: expiresAt, ReceivedQuantity: quantity, RemainingQuantity: quantity}
	appendFixture(t, ctx, store, "receive", "ReceiveDelivery", domain.Event{EventID: "event-delivery", RecordedAt: at, Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{Delivery: &delivery, Batches: []domain.Batch{batch}}})
	if consumed > 0 {
		order := domain.Order{OrderID: "order", ProposalID: "proposal", AcceptedPositions: []domain.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}, Allocations: []domain.Allocation{{ProductID: "p", BatchID: "batch", Quantity: consumed}}, PlacedAt: at}
		appendFixture(t, ctx, store, "order", "ConfirmOrder", domain.Event{EventID: "event-order", RecordedAt: at, Type: domain.EventOrderPlaced, Payload: domain.EventPayload{Order: &order}})
	}
	return store
}

func appendFixture(t *testing.T, ctx context.Context, store *memory.EventStore, commandID, commandType string, event domain.Event) {
	t.Helper()
	fingerprint := domain.Fingerprint(commandType, []byte(commandID))
	version, err := store.Version(ctx)
	if err != nil {
		t.Fatalf("Version(): %v", err)
	}
	_, err = store.AppendBatch(ctx, domain.CommandBatch{ExpectedVersion: version, CommandID: commandID, CommandType: commandType, CommandFingerprint: fingerprint, Events: []domain.Event{event}})
	if err != nil {
		t.Fatalf("AppendBatch(%q): %v", commandID, err)
	}
}

func moscowTime(year int, month time.Month, day, hour int) time.Time {
	return time.Date(year, month, day, hour, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
}

func TestReadCaptureDoesNotAlias(t *testing.T) {
	now := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, now.Add(time.Hour), 10000, 0)
	clock := &testClock{now: now}
	owner, err := NewOwner(store, clock.Now, func() string { return "id" })
	if err != nil {
		t.Fatal(err)
	}
	first, err := owner.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.ExpireBatch(context.Background(), expireRequest("batch"))
	if !errors.Is(err, ErrNotDue) {
		t.Fatalf("ExpireBatch() error = %v; want NotDue", err)
	}
	clock.Set(now.Add(time.Hour))
	if _, err := owner.ProcessDueExpirations(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := owner.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.StateVersion+1 != second.StateVersion {
		t.Fatalf("versions = %d then %d; want a single version advance", first.StateVersion, second.StateVersion)
	}
	first.State.Batches["batch"] = domain.Batch{}
	if got := second.State.Batches["batch"].RemainingQuantity; got != 0 {
		t.Fatalf("later capture remaining = %d; want 0 after expiry", got)
	}
}

func TestRecordedOrderWithDirtyProjection(t *testing.T) {
	now := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, now.Add(time.Hour), 10000, 6000)
	clock := &testClock{now: now}
	owner, err := NewOwner(store, clock.Now, func() string { return "id" })
	if err != nil {
		t.Fatal(err)
	}
	owner.usable = false
	order, found, err := owner.FindOrderByProposal(context.Background(), "proposal")
	if err != nil || !found || order.OrderID != "order" {
		t.Fatalf("FindOrderByProposal() = %#v, %v, %v; want recorded order before recovery", order, found, err)
	}
	if owner.usable {
		t.Fatal("lookup unexpectedly recovered the dirty projection")
	}
}

func TestPrepareReusesUsableProjection(t *testing.T) {
	now := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, now.Add(time.Hour), 10000, 0)
	reads := 0
	wrapped := &eventStoreWrapper{EventStore: base, read: func(_ int64, batches []domain.RecordedBatch, err error) ([]domain.RecordedBatch, error) {
		reads++
		return batches, err
	}}
	clock := &testClock{now: now}
	owner, err := NewOwner(wrapped, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("ReadBatches calls = %d; want only initial full recovery", reads)
	}
}

func TestPrepareRechecksDueAfterFinalVersionWait(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	clock := &testClock{now: dueAt.Add(-time.Nanosecond)}
	base := seedStore(t, dueAt, 10000, 0)
	wrapped := &versionAdvanceStore{EventStore: base, clock: clock, advanceOnCall: 2, advanceTo: dueAt}
	owner, err := NewOwner(wrapped, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	state, err := owner.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.EvaluatedAt.Equal(dueAt) || state.State.Batches["batch"].RemainingQuantity != 0 {
		t.Fatalf("capture = at %s, remaining %d; want fresh boundary time and expired stock", state.EvaluatedAt, state.State.Batches["batch"].RemainingQuantity)
	}
}

type versionAdvanceStore struct {
	EventStore
	clock         *testClock
	advanceOnCall int
	advanceTo     time.Time
	calls         int
}

func (s *versionAdvanceStore) Version(ctx context.Context) (int64, error) {
	s.calls++
	if s.calls == s.advanceOnCall {
		s.clock.Set(s.advanceTo)
	}
	return s.EventStore.Version(ctx)
}

func expireRequest(batchID string) contract.ExpireBatchRequest {
	return contract.ExpireBatchRequest{CommandID: "expiration/" + batchID, BatchID: batchID}
}

func TestNewOwnerRequiresDependencies(t *testing.T) {
	if _, err := NewOwner(nil, time.Now, func() string { return "id" }); err == nil {
		t.Fatal("NewOwner(nil, ...) succeeded; want invalid dependency error")
	}
}
