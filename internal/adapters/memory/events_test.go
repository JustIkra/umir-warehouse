package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/application"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestAppendBatchAtomic(t *testing.T) {
	ctx := context.Background()
	store := NewEventStore()
	first := domain.CommandBatch{
		CommandID:          "receive-delivery",
		CommandType:        "ReceiveDelivery",
		CommandFingerprint: "fingerprint-1",
		Events: []domain.Event{
			{EventID: "delivery-1", RecordedAt: testTime(), Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{Delivery: &domain.Delivery{DeliveryID: "delivery-1", Number: "invoice-1", Lines: []domain.DeliveryLine{{ProductID: "p1", Quantity: 1000}}}, Batches: []domain.Batch{{BatchID: "batch-1", DeliveryID: "delivery-1", ProductID: "p1", ExpiresAt: testTime(), ReceivedQuantity: 1000, RemainingQuantity: 1000}}}},
			{EventID: "expired-1", RecordedAt: testTime(), Type: domain.EventBatchExpired, Payload: domain.EventPayload{Expiry: &domain.ExpiryResult{BatchID: "batch-1", ProductID: "p1", WrittenOffQuantity: 1000, Reason: domain.ExpiryReasonExpiration, ExpiresAt: testTime(), ProcessedAt: testTime()}}},
		},
	}
	if _, err := store.AppendBatch(ctx, first); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Version(ctx); err != nil || got != 2 {
		t.Fatalf("Version() = %d, %v; want 2, nil", got, err)
	}

	conflict := domain.CommandBatch{
		ExpectedVersion:    0,
		CommandID:          "receive-other",
		CommandType:        "ReceiveDelivery",
		CommandFingerprint: "fingerprint-2",
		Events: []domain.Event{
			{EventID: "delivery-2", RecordedAt: testTime(), Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{Delivery: &domain.Delivery{DeliveryID: "delivery-2", Number: "invoice-2"}, Batches: []domain.Batch{{BatchID: "batch-2", DeliveryID: "delivery-2", ProductID: "p2", ExpiresAt: testTime(), ReceivedQuantity: 1000, RemainingQuantity: 1000}}}},
			{EventID: "expired-2", RecordedAt: testTime(), Type: domain.EventBatchExpired, Payload: domain.EventPayload{Expiry: &domain.ExpiryResult{BatchID: "batch-2", ProductID: "p2", WrittenOffQuantity: 1000, Reason: domain.ExpiryReasonExpiration, ExpiresAt: testTime(), ProcessedAt: testTime()}}},
		},
	}
	if _, err := store.AppendBatch(ctx, conflict); !errors.Is(err, domain.ErrConcurrentModification) {
		t.Fatalf("AppendBatch() error = %v; want ErrConcurrentModification", err)
	}
	if got, err := store.Version(ctx); err != nil || got != 2 {
		t.Fatalf("Version() after conflict = %d, %v; want 2, nil", got, err)
	}
	batches, err := store.ReadBatches(ctx, 0)
	if err != nil || len(batches) != 1 || len(batches[0].Events) != 2 {
		t.Fatalf("ReadBatches() = %#v, %v; want one complete two-event batch", batches, err)
	}
}

func testTime() time.Time {
	return time.Date(2026, time.March, 1, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
}

func TestCommandIdentity(t *testing.T) {
	ctx := context.Background()
	store := NewEventStore()
	first := productCommand("cmd-1", "fp-1", "e-1", "p1")
	if _, err := store.AppendBatch(ctx, first); err != nil {
		t.Fatal(err)
	}
	first.ExpectedVersion = 91
	result, err := store.AppendBatch(ctx, first)
	if err != nil || !result.Replayed {
		t.Fatalf("same identity retry = %#v, %v; want replay, nil", result, err)
	}
	if result.Batch.Events[0].EventID != "e-1" {
		t.Fatalf("replayed event ID = %q; want original e-1", result.Batch.Events[0].EventID)
	}
	if got, err := store.Version(ctx); err != nil || got != 1 {
		t.Fatalf("Version() after retry = %d, %v; want 1, nil", got, err)
	}
	conflict := productCommand("cmd-1", "fp-2", "e-2", "p2")
	if _, err := store.AppendBatch(ctx, conflict); !errors.Is(err, domain.ErrCommandIdentityConflict) {
		t.Fatalf("different fingerprint error = %v; want ErrCommandIdentityConflict", err)
	}
}

func TestAppendUnknownRetry(t *testing.T) {
	ctx := context.Background()
	store := NewEventStore()
	batch := productCommand("cmd-unknown", "fp", "same-event", "p1")
	wrapped := &loseFirstResponse{EventStore: store}
	if _, err := wrapped.AppendBatch(ctx, batch); !errors.Is(err, errResponseLost) {
		t.Fatalf("first AppendBatch() error = %v; want lost response", err)
	}
	result, err := wrapped.AppendBatch(ctx, batch)
	if err != nil || !result.Replayed {
		t.Fatalf("retry = %#v, %v; want original recorded result", result, err)
	}
	if len(result.Batch.Events) != 1 || result.Batch.Events[0].EventID != "same-event" {
		t.Fatalf("retry events = %#v; want original event ID", result.Batch.Events)
	}
	if got, err := store.Version(ctx); err != nil || got != 1 {
		t.Fatalf("Version() after unknown retry = %d, %v; want 1, nil", got, err)
	}
}

func TestAppendFailureBeforeCommitLeavesStoreEmpty(t *testing.T) {
	store := NewEventStore()
	wrapped := failBeforeCommit{EventStore: store}
	_, err := wrapped.AppendBatch(context.Background(), productCommand("cmd-before", "fp", "event-before", "p1"))
	if !errors.Is(err, errBeforeCommit) {
		t.Fatalf("AppendBatch() error = %v; want pre-commit failure", err)
	}
	if version, err := store.Version(context.Background()); err != nil || version != 0 {
		t.Fatalf("Version() after pre-commit failure = %d, %v; want 0, nil", version, err)
	}
	if _, ok, err := store.FindCommand(context.Background(), "cmd-before"); err != nil || ok {
		t.Fatalf("FindCommand() = %v, %v; failed write must leave no command", ok, err)
	}
}

func TestContextCancelledAfterCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := NewEventStore()
	wrapped := cancelAfterCommit{EventStore: store, cancel: cancel}
	result, err := wrapped.AppendBatch(ctx, productCommand("cmd-cancel", "fp", "e-cancel", "p1"))
	if err != nil || result.Batch.FinalSequence != 1 {
		t.Fatalf("AppendBatch() after cancellation = %#v, %v; want committed sequence 1", result, err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("ctx.Err() = %v; want context.Canceled", ctx.Err())
	}
	if got, err := store.Version(context.Background()); err != nil || got != 1 {
		t.Fatalf("Version() after cancellation = %d, %v; want committed version 1", got, err)
	}
}

func TestStoreCopyIsolation(t *testing.T) {
	ctx := context.Background()
	store := NewEventStore()
	dish := &domain.Dish{DishID: "d1", Name: "Soup", Ingredients: []domain.Ingredient{{ProductID: "p1", Quantity: 1000}}}
	farFuture := time.Date(10000, 1, 1, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	batch := domain.CommandBatch{CommandID: "cmd-copy", CommandType: "DefineDish", CommandFingerprint: "fp", Events: []domain.Event{{EventID: "e1", RecordedAt: farFuture, Type: domain.EventDishDefined, Payload: domain.EventPayload{Dish: dish}}}}
	result, err := store.AppendBatch(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	dish.Ingredients[0].Quantity = 9000
	result.Batch.Events[0].Payload.Dish.Ingredients[0].Quantity = 8000
	result.Command.Result.Dish.Ingredients[0].Quantity = 7000

	read, err := store.ReadBatches(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := read[0].Events[0].Payload.Dish.Ingredients[0].Quantity; got != 1000 {
		t.Fatalf("stored event ingredient quantity = %d; want original value", got)
	}
	if got := read[0].Events[0].RecordedAt.Year(); got != 10000 {
		t.Fatalf("stored timestamp year = %d; want representable year 10000", got)
	}
	command, ok, err := store.FindCommand(ctx, "cmd-copy")
	if err != nil || !ok || command.Result.Dish.Ingredients[0].Quantity != 1000 {
		t.Fatalf("FindCommand() = %#v, %v, %v; want isolated original result", command, ok, err)
	}
}

func TestEmptyBatchDoesNotRecordCommand(t *testing.T) {
	store := NewEventStore()
	_, err := store.AppendBatch(context.Background(), domain.CommandBatch{CommandID: "empty", CommandType: "RegisterProduct", CommandFingerprint: "fp"})
	if !errors.Is(err, domain.ErrInvalidEventBatch) {
		t.Fatalf("AppendBatch() error = %v; want ErrInvalidEventBatch", err)
	}
	if _, ok, err := store.FindCommand(context.Background(), "empty"); err != nil || ok {
		t.Fatalf("FindCommand() = %v, %v; empty command must not be recorded", ok, err)
	}
}

func TestProposalIndexReturnsCopy(t *testing.T) {
	store := NewEventStore()
	order := &domain.Order{
		OrderID: "order-1", ProposalID: "proposal-1", PlacedAt: testTime(),
		AcceptedPositions: []domain.AcceptedPosition{{LineNo: 1, DishID: "d1", Portions: 1}},
		Allocations:       []domain.Allocation{{ProductID: "p1", BatchID: "batch-1", Quantity: 1000}},
	}
	_, err := store.AppendBatch(context.Background(), domain.CommandBatch{
		CommandID: "confirm-1", CommandType: "ConfirmOrder", CommandFingerprint: "fp",
		Events: []domain.Event{{EventID: "order-event", RecordedAt: testTime(), Type: domain.EventOrderPlaced, Payload: domain.EventPayload{Order: order}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	order.Allocations[0].Quantity = 5
	found, ok, err := store.FindOrderByProposal(context.Background(), "proposal-1")
	if err != nil || !ok || found.Allocations[0].Quantity != 1000 {
		t.Fatalf("FindOrderByProposal() = %#v, %v, %v; want isolated saved order", found, ok, err)
	}
	found.Allocations[0].Quantity = 9
	foundAgain, ok, err := store.FindOrderByProposal(context.Background(), "proposal-1")
	if err != nil || !ok || foundAgain.Allocations[0].Quantity != 1000 {
		t.Fatalf("second FindOrderByProposal() = %#v, %v, %v; want original allocation", foundAgain, ok, err)
	}
}

func productCommand(commandID, fingerprint, eventID, productID string) domain.CommandBatch {
	return domain.CommandBatch{
		CommandID:          commandID,
		CommandType:        "RegisterProduct",
		CommandFingerprint: fingerprint,
		Events: []domain.Event{{
			EventID:    eventID,
			RecordedAt: testTime(),
			Type:       domain.EventProductRegistered,
			Payload:    domain.EventPayload{Product: &domain.Product{ProductID: productID, Name: "Tomato", BaseUnit: domain.BaseUnitG}},
		}},
	}
}

type loseFirstResponse struct {
	application.EventStore
	failed bool
}

var errResponseLost = errors.New("response lost")
var errBeforeCommit = errors.New("write rejected before commit")

type failBeforeCommit struct {
	application.EventStore
}

func (s failBeforeCommit) AppendBatch(context.Context, domain.CommandBatch) (domain.AppendResult, error) {
	return domain.AppendResult{}, errBeforeCommit
}

func (s *loseFirstResponse) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	result, err := s.EventStore.AppendBatch(ctx, batch)
	if err == nil && !s.failed {
		s.failed = true
		return domain.AppendResult{}, errResponseLost
	}
	return result, err
}

type cancelAfterCommit struct {
	application.EventStore
	cancel context.CancelFunc
}

func (s cancelAfterCommit) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	result, err := s.EventStore.AppendBatch(ctx, batch)
	s.cancel()
	return result, err
}
