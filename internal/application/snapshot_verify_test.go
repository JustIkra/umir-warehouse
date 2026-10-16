package application

import (
	"context"
	"fmt"
	"strings"
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
		bad       domain.Snapshot
		sink      *logSink
		verified  *int
		// good is the snapshot the journal gives now.
		good func() domain.Snapshot
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
		sink := newLogSink()
		if err := owner.ConfigureLogger(sink.logf); err != nil {
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
		good := func() domain.Snapshot {
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
			return snapshotFor(truth)
		}
		bad := good()
		// Balanced and structurally valid, but not what the journal produces.
		moved := bad.State.Batches["batch"]
		moved.RemainingQuantity, moved.ConsumedByOrders = 5000, 5000
		bad.State.Batches["batch"] = moved
		if err := domain.ValidateSnapshot(bad); err != nil {
			t.Fatalf("diverging candidate should be structurally valid: %v", err)
		}
		verified := 0
		owner.snapshotWorker.mu.Lock()
		verify := owner.snapshotWorker.verify
		owner.snapshotWorker.verify = func(ctx context.Context, candidate domain.Snapshot) error {
			verified++
			return verify(ctx, candidate)
		}
		owner.snapshotWorker.mu.Unlock()
		return fixture{store: store, snapshots: snapshots, worker: owner.snapshotWorker, good: good, bad: bad, sink: sink, verified: &verified}
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
			if !worked || err != nil {
				t.Fatalf("RunOnce() with a diverging candidate = %v, %v; want it worked and dropped without an error", worked, err)
			}
			if !strings.Contains(f.sink.text(), "does not match the journal") {
				t.Fatalf("the rejection was not logged: %q", f.sink.text())
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
			// The same journal position is not verified again: it would only
			// repeat the walk that failed.
			checked := *f.verified
			f.worker.Offer(f.bad)
			if worked, err := f.worker.RunOnce(ctx); worked || err != nil || *f.verified != checked {
				t.Fatalf("offering the rejected position again = %v, %v after %d more verifications; want none", worked, err, *f.verified-checked)
			}
			// A later position is judged on its own.
			appendFixture(t, ctx, f.store, "register-extra", "RegisterProduct", domain.Event{EventID: "event-extra", RecordedAt: at, Type: domain.EventProductRegistered, Payload: domain.EventPayload{Product: &domain.Product{ProductID: "extra", Name: "Extra", BaseUnit: domain.BaseUnitG}}})
			f.worker.Offer(f.good())
			if worked, err := f.worker.RunOnce(ctx); !worked || err != nil {
				t.Fatalf("RunOnce() with a matching candidate = %v, %v", worked, err)
			}
			if sequence, remaining := latest(t, f); sequence != 5 || remaining != 4000 {
				t.Fatalf("latest snapshot = %d/%d; want 5/4000", sequence, remaining)
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
		f.worker.Offer(f.good())
		if worked, err := f.worker.RunOnce(ctx); !worked || err != nil {
			t.Fatalf("RunOnce() = %v, %v; a correct candidate must replace a wrong previous snapshot", worked, err)
		}
		if sequence, remaining := latest(t, f); sequence != 4 || remaining != 4000 {
			t.Fatalf("latest snapshot = %d/%d; want 4/4000", sequence, remaining)
		}
	})
}

// A candidate the journal contradicts must not stop the worker that serves the
// snapshots of later positions.
func TestSnapshotWorkerSurvivesRejectedCandidate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	at := moscowTime(2026, time.April, 1, 10)
	store := seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)
	snapshots := memory.NewSnapshotStore()
	owner, err := NewOwner(store, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	sink := newLogSink()
	if err := owner.ConfigureLogger(sink.logf); err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(snapshots, 100); err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- owner.RunSnapshots(ctx) }()

	bad := snapshotFor(owner.state)
	moved := bad.State.Batches["batch"]
	moved.RemainingQuantity, moved.ConsumedByOrders = 5000, 5000
	bad.State.Batches["batch"] = moved
	owner.snapshotWorker.Offer(bad)
	sink.waitFor(t, "does not match the journal")

	appendFixture(t, ctx, store, "register-extra", "RegisterProduct", domain.Event{EventID: "event-extra", RecordedAt: at, Type: domain.EventProductRegistered, Payload: domain.EventPayload{Product: &domain.Product{ProductID: "extra", Name: "Extra", BaseUnit: domain.BaseUnitG}}})
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
	owner.snapshotWorker.Offer(snapshotFor(truth))
	deadline := time.After(10 * time.Second)
	for {
		if saved, found, err := snapshots.LoadLatest(ctx); err != nil {
			t.Fatal(err)
		} else if found && saved.LastSequence == 4 {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("the snapshot worker stopped after a rejected candidate: %v", err)
		case <-deadline:
			t.Fatal("the later snapshot was never saved")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// bulkSnapshot is a snapshot of a state with n batches of one product.
func bulkSnapshot(n int) domain.Snapshot {
	at := moscowTime(2026, time.April, 1, 10)
	state := domain.InventoryState{
		Products:     map[string]domain.Product{"p": {ProductID: "p", Name: "P", BaseUnit: domain.BaseUnitG}},
		Dishes:       map[string]domain.Dish{},
		DeliveryIDs:  map[string]struct{}{},
		Batches:      map[string]domain.Batch{},
		StateVersion: int64(n) + 2,
	}
	for i := range n {
		id := fmt.Sprintf("b%d", i)
		state.DeliveryIDs["d"+id] = struct{}{}
		state.Batches[id] = domain.Batch{BatchID: id, DeliveryID: "d" + id, LineNo: 1, ProductID: "p", ManufacturedAt: at, ShelfLife: domain.ShelfLife{Value: 5, Unit: domain.ShelfLifeDay}, ReceivedAt: at, ExpiresAt: at.AddDate(0, 0, 5), ReceivedQuantity: 10, RemainingQuantity: 10}
	}
	return domain.Snapshot{SchemaVersion: domain.SnapshotSchemaV2, StreamID: domain.InventoryStreamID, LastSequence: state.StateVersion, State: state}
}

// An offer nobody needs costs nothing, and one that is kept is copied once:
// running it does not copy the state again.
func TestSnapshotOfferCopiesOnlyWhatIsKept(t *testing.T) {
	big, small := bulkSnapshot(2000), bulkSnapshot(20)
	t.Run("stale offer", func(t *testing.T) {
		worker := NewSnapshotWorker(memory.NewSnapshotStore())
		worker.observeSaved(big.LastSequence)
		if allocs := testing.AllocsPerRun(20, func() { worker.Offer(big) }); allocs > 1 {
			t.Fatalf("an offer behind the saved snapshot allocates %.0f times; want at most 1", allocs)
		}
	})
	t.Run("run", func(t *testing.T) {
		allocsOfRun := func(snapshot domain.Snapshot) float64 {
			var total float64
			const runs = 20
			for i := range runs {
				worker := NewSnapshotWorker(memory.NewSnapshotStore())
				worker.verify = func(context.Context, domain.Snapshot) error { return nil }
				snapshot.State.StateVersion += int64(i)
				snapshot.LastSequence = snapshot.State.StateVersion
				worker.Offer(snapshot)
				total += testing.AllocsPerRun(1, func() {
					if _, err := worker.RunOnce(context.Background()); err != nil {
						t.Fatal(err)
					}
				})
			}
			return total / runs
		}
		// Save itself copies the snapshot once, so compare the owner's own share:
		// running must cost about what Save costs, not twice it.
		base := allocsOfRun(small)
		if large := allocsOfRun(big); large > base*1.2+50 && large > 2*float64(len(big.State.Batches))+50 {
			t.Fatalf("running a snapshot of 2000 batches allocates %.0f times against %.0f for 20; want no extra copy of the state", large, base)
		}
	})
}

// Storage hands back a snapshot state without the owner's derived index; the
// owner indexes it once on recovery so commits do not search every batch.
func TestRecoveryFromSnapshotIndexesBatches(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	store := seedStore(t, at.Add(72*time.Hour), 20000, 0)
	seed, err := NewOwner(store, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	// A state rebuilt from plain data, as a decoder would produce it.
	plain := domain.InventoryState{Products: seed.state.Products, Dishes: seed.state.Dishes, DeliveryIDs: seed.state.DeliveryIDs, Batches: seed.state.Batches, StateVersion: seed.state.StateVersion}
	if plain.Indexed() {
		t.Fatal("a plain state reports an index")
	}
	snapshots := &plainSnapshotStore{snapshot: domain.Snapshot{SchemaVersion: domain.SnapshotSchemaV2, StreamID: domain.InventoryStreamID, LastSequence: plain.StateVersion, State: plain}}
	owner, err := NewOwner(store, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(snapshots, 100); err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if owner.lastSavedSequence != plain.StateVersion {
		t.Fatalf("recovered from sequence %d; want the snapshot at %d", owner.lastSavedSequence, plain.StateVersion)
	}
	if !owner.state.Indexed() {
		t.Fatal("the state recovered from a snapshot has no batch index")
	}
}

// plainSnapshotStore returns its snapshot as it is, without the copy that
// would give the state an index.
type plainSnapshotStore struct{ snapshot domain.Snapshot }

func (s *plainSnapshotStore) LoadLatest(context.Context) (domain.Snapshot, bool, error) {
	return s.snapshot, true, nil
}

func (s *plainSnapshotStore) Save(context.Context, domain.Snapshot) error { return nil }
