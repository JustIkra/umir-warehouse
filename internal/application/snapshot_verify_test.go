package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestSnapshotIsVerifiedAgainstJournalBeforeSave(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	type fixture struct {
		store     *memory.EventStore
		snapshots *memory.SnapshotStore
		worker    *SnapshotWorker
		good, bad domain.Snapshot
	}
	// A journal of four batches where the owner has saved (or not) the
	// snapshot at sequence 3; good and bad are candidates at sequence 4.
	build := func(t *testing.T, savePrevious bool) fixture {
		t.Helper()
		store := seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)
		snapshots := memory.NewSnapshotStore()
		owner, err := NewOwner(store, func() time.Time { return at }, nextTestID())
		if err != nil {
			t.Fatal(err)
		}
		if err := owner.ConfigureSnapshots(snapshots, 3); err != nil {
			t.Fatal(err)
		}
		if err := owner.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if savePrevious {
			if worked, err := owner.RunSnapshotOnce(ctx); err != nil || !worked {
				t.Fatalf("save previous snapshot = %v, %v", worked, err)
			}
		}
		order := domain.Order{OrderID: "order", ProposalID: "proposal", AcceptedPositions: []domain.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}, Allocations: []domain.Allocation{{ProductID: "p", BatchID: "batch", Quantity: 6000}}, PlacedAt: at}
		appendFixture(t, ctx, store, "order", "ConfirmOrder", domain.Event{EventID: "event-order", RecordedAt: at, Type: domain.EventOrderPlaced, Payload: domain.EventPayload{Order: &order}})
		batches, err := store.ReadBatches(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		var truth domain.InventoryState
		for _, batch := range batches {
			if err := truth.ApplyBatch(batch); err != nil {
				t.Fatal(err)
			}
		}
		good := snapshotFor(truth)
		bad := snapshotFor(truth)
		// Balanced and structurally valid, but not what the journal produces.
		moved := bad.State.Batches["batch"]
		moved.RemainingQuantity, moved.ConsumedByOrders = 5000, 5000
		bad.State.Batches["batch"] = moved
		if err := domain.ValidateSnapshot(bad); err != nil {
			t.Fatalf("diverging candidate should be structurally valid: %v", err)
		}
		return fixture{store: store, snapshots: snapshots, worker: owner.snapshotWorker, good: good, bad: bad}
	}
	latest := func(t *testing.T, f fixture) (int64, domain.Quantity) {
		t.Helper()
		snapshot, found, err := f.snapshots.LoadLatest(ctx)
		if err != nil || !found {
			t.Fatalf("LoadLatest() = found %v, err %v", found, err)
		}
		return snapshot.LastSequence, snapshot.State.Batches["batch"].RemainingQuantity
	}
	hasSnapshot := func(t *testing.T, f fixture) bool {
		t.Helper()
		_, found, err := f.snapshots.LoadLatest(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	for _, test := range []struct {
		name         string
		savePrevious bool
	}{{"with previous snapshot", true}, {"without previous snapshot", false}} {
		t.Run(test.name, func(t *testing.T) {
			f := build(t, test.savePrevious)
			wantSequence, wantRemaining := int64(3), domain.Quantity(10000)
			if !test.savePrevious {
				// The owner's own candidate at 3 was never saved.
				if hasSnapshot(t, f) {
					t.Fatal("unexpected previous snapshot")
				}
			}
			f.worker.Offer(f.bad)
			worked, err := f.worker.RunOnce(ctx)
			if !worked || !errors.Is(err, domain.ErrInvalidSnapshot) {
				t.Fatalf("RunOnce() with a diverging candidate = %v, %v; want ErrInvalidSnapshot", worked, err)
			}
			if test.savePrevious {
				if sequence, remaining := latest(t, f); sequence != wantSequence || remaining != wantRemaining {
					t.Fatalf("latest snapshot = %d/%d; want the previous %d/%d", sequence, remaining, wantSequence, wantRemaining)
				}
			} else if hasSnapshot(t, f) {
				t.Fatal("diverging candidate was saved")
			}
			if worked, err := f.worker.RunOnce(ctx); worked || err != nil {
				t.Fatalf("second RunOnce() = %v, %v; a rejected candidate must not be retried", worked, err)
			}
			f.worker.Offer(f.good)
			if worked, err := f.worker.RunOnce(ctx); !worked || err != nil {
				t.Fatalf("RunOnce() with a matching candidate = %v, %v", worked, err)
			}
			if sequence, remaining := latest(t, f); sequence != 4 || remaining != 4000 {
				t.Fatalf("latest snapshot = %d/%d; want 4/4000", sequence, remaining)
			}
		})
	}
	t.Run("previous snapshot contradicts the journal", func(t *testing.T) {
		f := build(t, false)
		// A balanced but wrong variant of the real state at sequence 3.
		batches, err := f.store.ReadBatches(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		var prefix domain.InventoryState
		for _, batch := range batches[:3] {
			if err := prefix.ApplyBatch(batch); err != nil {
				t.Fatal(err)
			}
		}
		moved := prefix.Batches["batch"]
		moved.RemainingQuantity, moved.ConsumedByOrders = 9000, 1000
		prefix.Batches["batch"] = moved
		if err := f.snapshots.Save(ctx, snapshotFor(prefix)); err != nil {
			t.Fatal(err)
		}
		f.worker.Offer(f.good)
		if worked, err := f.worker.RunOnce(ctx); !worked || err != nil {
			t.Fatalf("RunOnce() = %v, %v; a correct candidate must replace a wrong previous snapshot", worked, err)
		}
		if sequence, remaining := latest(t, f); sequence != 4 || remaining != 4000 {
			t.Fatalf("latest snapshot = %d/%d; want 4/4000", sequence, remaining)
		}
	})
}
