package application

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

// journalCountingStore records which journal ranges an Owner reads and can
// fail scans on demand.
type journalCountingStore struct {
	EventStore
	mu      sync.Mutex
	scans   []int64
	reads   []int64
	scanErr func(after int64) error
}

func (s *journalCountingStore) ScanBatches(ctx context.Context, after int64, fn func(domain.RecordedBatch) error) error {
	s.mu.Lock()
	s.scans = append(s.scans, after)
	failure := s.scanErr
	s.mu.Unlock()
	if failure != nil {
		if err := failure(after); err != nil {
			return err
		}
	}
	return s.EventStore.ScanBatches(ctx, after, fn)
}

func (s *journalCountingStore) ReadBatches(ctx context.Context, after int64) ([]domain.RecordedBatch, error) {
	s.mu.Lock()
	s.reads = append(s.reads, after)
	s.mu.Unlock()
	return s.EventStore.ReadBatches(ctx, after)
}

func (s *journalCountingStore) readsFromStart() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, after := range append(append([]int64(nil), s.scans...), s.reads...) {
		if after == 0 {
			count++
		}
	}
	return count
}

func (s *journalCountingStore) setScanErr(failure func(int64) error) {
	s.mu.Lock()
	s.scanErr = failure
	s.mu.Unlock()
}

func TestRecoveryFromSnapshotDoesNotReadPrefix(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	store := seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)
	snapshots := memory.NewSnapshotStore()
	seed, err := NewOwner(store, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.ConfigureSnapshots(snapshots, 2); err != nil {
		t.Fatal(err)
	}
	if err := seed.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if worked, err := seed.RunSnapshotOnce(ctx); err != nil || !worked {
		t.Fatalf("RunSnapshotOnce() = %v, %v; want a saved snapshot", worked, err)
	}
	appendFixture(t, ctx, store, "register-extra", "RegisterProduct", domain.Event{EventID: "event-extra", RecordedAt: at, Type: domain.EventProductRegistered, Payload: domain.EventPayload{Product: &domain.Product{ProductID: "extra", Name: "Extra", BaseUnit: domain.BaseUnitG}}})

	counting := &journalCountingStore{EventStore: store}
	owner, err := NewOwner(counting, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(snapshots, 2); err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if owner.state.StateVersion != 4 || owner.lastSavedSequence != 3 {
		t.Fatalf("recovered version %d from snapshot %d; want 4 from 3", owner.state.StateVersion, owner.lastSavedSequence)
	}
	if reads := counting.readsFromStart(); reads != 0 {
		t.Fatalf("recovery from a snapshot read the journal from the start %d times; want 0", reads)
	}
}

func TestFailedRecoveryKeepsPreviousProjection(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	counting := &journalCountingStore{EventStore: seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)}
	owner, err := NewOwner(counting, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	before := domain.CloneInventoryState(owner.state)
	expirations := len(owner.expirations)
	boom := errors.New("journal unavailable")
	counting.setScanErr(func(int64) error { return boom })

	owner.mu.Lock()
	err = owner.recoverLocked(ctx)
	usable := owner.usable
	owner.mu.Unlock()

	if !errors.Is(err, boom) {
		t.Fatalf("recoverLocked() error = %v; want the journal error", err)
	}
	if !usable {
		t.Fatal("a failed recovery marked the previous projection unusable")
	}
	if !domain.EqualInventoryState(before, owner.state) || len(owner.expirations) != expirations {
		t.Fatalf("failed recovery changed the projection: %#v", owner.state)
	}
}

func TestSnapshotTailFailureFallsBackToJournal(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	store := seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 6000)
	batches, err := store.ReadBatches(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var prefix domain.InventoryState
	for _, batch := range batches[:3] {
		if err := prefix.ApplyBatch(batch); err != nil {
			t.Fatal(err)
		}
	}
	// Structurally valid, but the order in the tail consumes 6000 of a batch
	// that holds only 5000 here, so the tail cannot be applied.
	short := prefix.Batches["batch"]
	short.RemainingQuantity, short.ConsumedByOrders = 5000, 5000
	prefix.Batches["batch"] = short
	candidate := snapshotFor(prefix)
	if err := domain.ValidateSnapshot(candidate); err != nil {
		t.Fatalf("altered snapshot should be structurally valid: %v", err)
	}
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	owner, err := NewOwner(store, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(&fixedSnapshotStore{snapshot: candidate, found: true}, 2); err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(ctx); err != nil {
		t.Fatalf("Recover() = %v; want fallback to the full journal", err)
	}
	if got := owner.state.Batches["batch"].RemainingQuantity; got != 4000 {
		t.Fatalf("remaining after fallback = %d; want 4000 from the journal", got)
	}
	if owner.lastSavedSequence != 0 {
		t.Fatalf("rejected snapshot counted as saved at %d", owner.lastSavedSequence)
	}
	if !strings.Contains(logged.String(), "snapshot") {
		t.Fatalf("fallback was not logged: %q", logged.String())
	}
}

func TestSnapshotPlusTailEqualsFullReplay(t *testing.T) {
	for seed := uint64(1); seed <= 24; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) { checkSnapshotPlusTail(t, seed) })
	}
}

func checkSnapshotPlusTail(t *testing.T, seed uint64) {
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(seed, 7))
	scoped := seed%2 == 0
	clock := &testClock{now: moscowTime(2026, time.April, 1, 8)}
	store := memory.NewEventStore()
	snapshots := memory.NewSnapshotStore()
	newOwner := func(prefix string, events EventStore, snaps SnapshotStore) *Owner {
		t.Helper()
		next := 0
		owner, err := NewOwner(events, clock.Now, func() string { next++; return fmt.Sprintf("%s-%d", prefix, next) })
		if err != nil {
			t.Fatal(err)
		}
		if scoped {
			if err := owner.ConfigureScope(w3Scope()); err != nil {
				t.Fatal(err)
			}
		}
		if snaps != nil {
			if err := owner.ConfigureSnapshots(snaps, int64(1+rng.IntN(5))); err != nil {
				t.Fatal(err)
			}
		}
		return owner
	}
	live := newOwner("live", store, snapshots)
	if err := live.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	saveSnapshots := func() {
		t.Helper()
		for {
			worked, err := live.RunSnapshotOnce(ctx)
			if err != nil {
				t.Fatalf("RunSnapshotOnce() = %v", err)
			}
			if !worked {
				return
			}
		}
	}
	var products, dishes int
	var commands []string
	command := func() string {
		id := fmt.Sprintf("cmd-%d-%d", seed, len(commands))
		commands = append(commands, id)
		return id
	}
	for step := 0; step < 70+rng.IntN(50); step++ {
		switch op := rng.IntN(10); {
		case op < 2 && products < 8:
			if _, err := live.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: command(), ProductID: fmt.Sprintf("p%d", products), Name: fmt.Sprintf("Product %d", products), BaseUnit: contract.BaseUnitG}); err != nil {
				t.Fatalf("step %d RegisterProduct: %v", step, err)
			}
			products++
		case op < 3 && products > 0:
			ingredients := make([]contract.Ingredient, 0, 3)
			for _, index := range rng.Perm(products)[:1+rng.IntN(min(3, products))] {
				ingredients = append(ingredients, contract.Ingredient{ProductID: fmt.Sprintf("p%d", index), Quantity: contract.Quantity(100 + rng.IntN(800))})
			}
			if _, err := live.DefineDish(ctx, contract.DefineDishRequest{CommandID: command(), DishID: fmt.Sprintf("d%d", dishes), Name: fmt.Sprintf("Dish %d", dishes), Ingredients: ingredients}); err != nil {
				t.Fatalf("step %d DefineDish: %v", step, err)
			}
			dishes++
		case op < 5 && products > 0:
			now := clock.Now()
			lines := make([]contract.DeliveryLine, 0, products)
			for _, index := range rng.Perm(products)[:1+rng.IntN(products)] {
				lines = append(lines, contract.DeliveryLine{ProductID: fmt.Sprintf("p%d", index), ManufacturedAt: now.Add(-time.Duration(rng.IntN(12)) * time.Hour), ShelfLife: contract.ShelfLife{Value: int64(1 + rng.IntN(3)), Unit: contract.ShelfLifeDay}, ReceivedAt: now, Quantity: contract.Quantity(1000 + rng.IntN(8000))})
			}
			id := command()
			if _, err := live.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: id, DeliveryID: id, Number: "N", CreatedAt: now, Lines: lines}); err != nil {
				t.Fatalf("step %d ReceiveDelivery: %v", step, err)
			}
		case op < 7 && dishes > 0:
			positions := []contract.Position{{LineNo: 1, DishID: fmt.Sprintf("d%d", rng.IntN(dishes)), Portions: int64(1 + rng.IntN(3))}}
			evaluation, err := live.EvaluateOrder(ctx, contract.EvaluateOrderRequest{Positions: positions})
			if err != nil {
				t.Fatalf("step %d EvaluateOrder: %v", step, err)
			}
			if len(evaluation.AcceptedPositions) == 0 {
				continue
			}
			id := command()
			if _, err := live.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: id, ProposalID: "proposal-" + id, AcceptedPositions: evaluation.AcceptedPositions}); err != nil {
				t.Fatalf("step %d CommitAcceptedOrder: %v", step, err)
			}
		case op < 8:
			clock.Set(clock.Now().Add(time.Duration(10+rng.IntN(1200)) * time.Minute))
		case op < 9:
			if _, err := live.GetInventory(ctx); err != nil {
				t.Fatalf("step %d GetInventory: %v", step, err)
			}
		default:
			if _, err := live.RunExpirations(ctx); err != nil {
				t.Fatalf("step %d RunExpirations: %v", step, err)
			}
			if scoped {
				view, err := live.Prepare(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for batchID, batch := range view.State.Batches {
					if !batch.PendingWriteoff {
						continue
					}
					if _, err := live.ConfirmWriteoff(ctx, contract.ConfirmWriteoffRequest{CommandID: command(), BatchID: batchID, ActorUserID: w3Actor}); err != nil {
						t.Fatalf("step %d ConfirmWriteoff: %v", step, err)
					}
					break
				}
			}
		}
		saveSnapshots()
	}
	// Settle lazy expiry at the final clock so recovering owners find nothing due.
	if _, err := live.RunExpirations(ctx); err != nil {
		t.Fatal(err)
	}
	saveSnapshots()

	fromSnapshot := newOwner("snapshot", store, snapshots)
	fromJournal := newOwner("journal", store, nil)
	for _, owner := range []*Owner{fromSnapshot, fromJournal} {
		if err := owner.Recover(ctx); err != nil {
			t.Fatal(err)
		}
	}
	saved, found, err := snapshots.LoadLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if found && fromSnapshot.lastSavedSequence != saved.LastSequence {
		t.Fatalf("restored owner used snapshot %d; want latest %d", fromSnapshot.lastSavedSequence, saved.LastSequence)
	}
	for name, restored := range map[string]*Owner{"snapshot+tail": fromSnapshot, "journal": fromJournal} {
		compareOwners(t, name, live, restored, commands)
	}
}

func compareOwners(t *testing.T, name string, want, got *Owner, commands []string) {
	t.Helper()
	ctx := context.Background()
	if got.state.StateVersion != want.state.StateVersion || !domain.EqualInventoryState(want.state, got.state) {
		t.Fatalf("%s: state differs from the continuous owner\nwant %#v\ngot  %#v", name, want.state, got.state)
	}
	if len(got.expirations) != len(want.expirations) {
		t.Fatalf("%s: expiration index has %d entries; want %d", name, len(got.expirations), len(want.expirations))
	}
	for id, dueAt := range want.expirations {
		if other, ok := got.expirations[id]; !ok || !other.Equal(dueAt) {
			t.Fatalf("%s: expiration of %q = %v/%v; want %v", name, id, other, ok, dueAt)
		}
	}
	for id, batch := range want.state.Batches {
		if got.state.Batches[id].PendingWriteoff != batch.PendingWriteoff {
			t.Fatalf("%s: pending flag of %q differs", name, id)
		}
	}
	wantInventory, err := want.GetInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotInventory, err := got.GetInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantInventory, gotInventory) {
		t.Fatalf("%s: inventory differs\nwant %+v\ngot  %+v", name, wantInventory, gotInventory)
	}
	wantMenu, err := want.EvaluateMenu(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotMenu, err := got.EvaluateMenu(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantMenu, gotMenu) {
		t.Fatalf("%s: menu allocations differ\nwant %+v\ngot  %+v", name, wantMenu, gotMenu)
	}
	for _, id := range commands {
		wantRecord, wantFound, err := want.FindRecordedCommand(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		gotRecord, gotFound, err := got.FindRecordedCommand(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if wantFound != gotFound || !reflect.DeepEqual(wantRecord, gotRecord) {
			t.Fatalf("%s: command %q result differs", name, id)
		}
	}
}
