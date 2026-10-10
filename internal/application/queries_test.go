package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestVersionConflictAtExpiry(t *testing.T) {
	ctx := context.Background()
	expiresAt := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, expiresAt, 10000, 0)
	clock := &sequenceClock{times: []time.Time{expiresAt.Add(-time.Second), expiresAt.Add(-time.Second), expiresAt, expiresAt, expiresAt}}
	wrapper := &conflictOnceEventStore{EventStore: store}
	owner, err := NewOwner(wrapper, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "order-at-expiry", ProposalID: "proposal-at-expiry", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != contract.CommitResultRecheckRequired || result.Order != nil {
		t.Fatalf("commit after version conflict = %#v; want recheck after expiry", result)
	}
	if result.Evaluation == nil || !result.Evaluation.EvaluatedAt.Equal(expiresAt) || len(result.Evaluation.AcceptedPositions) != 0 {
		t.Fatalf("re-evaluation = %#v; want dueAt capture with no accepted positions", result.Evaluation)
	}
	if wrapper.conflicts != 1 {
		t.Fatalf("ConfirmOrder append conflicts = %d; want one", wrapper.conflicts)
	}
}

func TestHistoryWhenUnavailable(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, now.Add(time.Hour), 10000, 0)
	wrapper := &failingVersionEventStore{EventStore: base}
	owner, err := NewOwner(wrapper, func() time.Time { return now }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.GetInventory(ctx); !errors.Is(err, contract.ErrInventoryUnavailable) {
		t.Fatalf("GetInventory() error = %v; want InventoryUnavailable", err)
	}
	history, err := owner.GetHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if history.StateVersion != 3 || len(history.Events) != 3 {
		t.Fatalf("GetHistory() = %#v; want three facts through version 3", history)
	}
	if history.Events[0].Sequence != 1 || history.Events[0].CommandID != "register-p" || history.Events[2].Payload.Delivery == nil || history.Events[2].Payload.Delivery.DeliveryID != "delivery" || len(history.Events[2].Payload.Batches) != 1 {
		t.Fatalf("history facts = %#v; want full ordered event envelope and payload", history.Events)
	}
	history.Events[0].Payload.Product.Name = "Changed"
	again, err := owner.GetHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Events[0].Payload.Product.Name; got != "Product" {
		t.Fatalf("stored history product after caller mutation = %q; want Product", got)
	}
}

type conflictOnceEventStore struct {
	EventStore
	conflicts int
}

func (s *conflictOnceEventStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	if batch.CommandType == "ConfirmOrder" && s.conflicts == 0 {
		s.conflicts++
		return domain.AppendResult{}, domain.ErrConcurrentModification
	}
	return s.EventStore.AppendBatch(ctx, batch)
}

type failingVersionEventStore struct {
	EventStore
}

func (s *failingVersionEventStore) Version(context.Context) (int64, error) {
	return 0, errors.New("projection version unavailable")
}

func TestGetInventoryReturnsDetachedValues(t *testing.T) {
	now := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, now.Add(time.Hour), 10000, 0)
	owner, err := NewOwner(store, func() time.Time { return now }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	first, err := owner.GetInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first.Products[0].Batches[0].RemainingQuantity = 1
	second, err := owner.GetInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Products[0].Batches[0].RemainingQuantity; got != 10000 {
		t.Fatalf("remaining quantity after mutating first result = %d; want 10000", got)
	}
}

func TestGetExpiringBatchesUsesFloorAndCutoff(t *testing.T) {
	now := moscowTime(2026, time.April, 2, 10)
	expiresAt := now.Add(90*time.Minute + 900*time.Millisecond)
	store := seedStore(t, expiresAt, 10000, 0)
	owner, err := NewOwner(store, func() time.Time { return now }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.GetExpiringBatches(context.Background(), now.Add(-time.Second)); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatalf("GetExpiringBatches(past cutoff) error = %v; want InvalidArgument", err)
	}
	result, err := owner.GetExpiringBatches(context.Background(), expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].RemainingSeconds != 5400 || result.Items[0].IsExpired {
		t.Fatalf("GetExpiringBatches() = %#v; want one nonexpired batch with 5400 seconds", result.Items)
	}
}
