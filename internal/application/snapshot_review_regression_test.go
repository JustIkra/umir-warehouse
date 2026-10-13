package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestRunRetriesFailedCandidateWithoutNewEvents(t *testing.T) {
	base := memory.NewSnapshotStore()
	worker := NewSnapshotWorker(&failOnceSnapshotStore{SnapshotStore: base})
	worker.Offer(emptySnapshot(3))
	if err := worker.Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil; want injected save error")
	}
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("retry after Run error = %v, %v; candidate should remain queued", worked, err)
	}
	if snapshot, found, err := base.LoadLatest(context.Background()); err != nil || !found || snapshot.LastSequence != 3 {
		t.Fatalf("saved candidate = seq %d, found %v, err %v; want retried sequence 3", snapshot.LastSequence, found, err)
	}
}

func TestRunRetriesUnknownCommittedCandidate(t *testing.T) {
	base := memory.NewSnapshotStore()
	worker := NewSnapshotWorker(&commitThenErrorSnapshotStore{SnapshotStore: base})
	worker.Offer(emptySnapshot(3))
	if err := worker.Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil; want unknown Save outcome")
	}
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("retry after unknown Run error = %v, %v; exact candidate should remain available", worked, err)
	}
	if snapshot, found, err := base.LoadLatest(context.Background()); err != nil || !found || snapshot.LastSequence != 3 {
		t.Fatalf("latest snapshot = seq %d, found %v, err %v; want exact candidate sequence 3", snapshot.LastSequence, found, err)
	}
}

func TestSnapshotAuditRejectsExpiryBeforeLaterOrder(t *testing.T) {
	ctx := context.Background()
	due := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, due, 10000, 6000)
	expiry := domain.ExpiryResult{BatchID: "batch", ProductID: "p", WrittenOffQuantity: 4000, Reason: domain.ExpiryReasonExpiration, ExpiresAt: due, ProcessedAt: due}
	appendFixture(t, ctx, store, "expiry", "ExpireBatch", domain.Event{EventID: "review-expiry", RecordedAt: due, Type: domain.EventBatchExpired, Payload: domain.EventPayload{Expiry: &expiry}})
	batches, err := store.ReadBatches(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	state := domain.InventoryState{}
	for _, batch := range batches {
		state, err = domain.ApplyRecordedBatch(state, batch)
		if err != nil {
			t.Fatal(err)
		}
	}
	candidate := snapshotFor(state)
	batches[3], batches[4] = batches[4], batches[3]
	for i := 3; i < 5; i++ {
		batches[i].Events[0].Sequence = int64(i + 1)
		batches[i].FinalSequence = int64(i + 1)
	}
	if err := validateJournalContext(context.Background(), batches, 5); err != nil {
		t.Fatalf("structural journal validation = %v; want sequence-valid reordered prefix", err)
	}
	fullReplay := domain.InventoryState{}
	for _, batch := range batches {
		fullReplay, err = domain.ApplyRecordedBatch(fullReplay, batch)
		if err != nil {
			break
		}
	}
	if err == nil {
		t.Fatal("reordered journal unexpectedly passed full replay")
	}
	if err := auditSnapshotPrefix(candidate, batches); err == nil {
		t.Fatal("snapshot audit accepted expiry 4000 before order 6000 from a 10000 receipt")
	}
}

type commitThenErrorSnapshotStore struct {
	SnapshotStore
	mu   sync.Mutex
	done bool
}

func (s *commitThenErrorSnapshotStore) Save(ctx context.Context, snapshot domain.Snapshot) error {
	if err := s.SnapshotStore.Save(ctx, snapshot); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done {
		s.done = true
		return errors.New("save outcome unknown")
	}
	return nil
}
