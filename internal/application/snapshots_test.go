package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestSnapshotFullCommandBoundary(t *testing.T) {
	store := seedStore(t, moscowTime(2026, time.April, 2, 10), 10000, 0)
	snapshots := memory.NewSnapshotStore()
	owner, err := NewOwner(store, func() time.Time { return moscowTime(2026, time.April, 1, 10) }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(snapshots, 2); err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if saved, err := owner.RunSnapshotOnce(context.Background()); err != nil || !saved {
		t.Fatalf("RunSnapshotOnce() = %v, %v; want one full snapshot", saved, err)
	}
	snapshot, found, err := snapshots.LoadLatest(context.Background())
	if err != nil || !found {
		t.Fatalf("LoadLatest() = found %v, err %v", found, err)
	}
	if snapshot.LastSequence != 3 || snapshot.LastSequence != snapshot.State.StateVersion {
		t.Fatalf("snapshot sequence/state version = %d/%d; want full command boundary 3", snapshot.LastSequence, snapshot.State.StateVersion)
	}
	owner.mu.Lock()
	lastSaved := owner.lastSavedSequence
	owner.mu.Unlock()
	if lastSaved != 3 {
		t.Fatalf("confirmed saved sequence = %d; want monotonic acknowledgment 3", lastSaved)
	}
}

func TestSlowSaveCopyIsolation(t *testing.T) {
	base := memory.NewSnapshotStore()
	slow := &gatedSnapshotStore{SnapshotStore: base, entered: make(chan domain.Snapshot, 1), release: make(chan struct{})}
	store := seedStore(t, moscowTime(2026, time.April, 2, 10), 10000, 0)
	owner, err := NewOwner(store, func() time.Time { return moscowTime(2026, time.April, 1, 10) }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(slow, 2); err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := owner.RunSnapshotOnce(context.Background())
		done <- err
	}()
	var captured domain.Snapshot
	select {
	case captured = <-slow.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot Save did not start")
	}
	if _, err := owner.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "later", ProductID: "later-product", Name: "Later", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatalf("RegisterProduct while Save blocked = %v; want owner progress", err)
	}
	if _, exists := captured.State.Products["later-product"]; exists {
		t.Fatal("captured snapshot changed after owner published a later command")
	}
	close(slow.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotQueueBound(t *testing.T) {
	base := memory.NewSnapshotStore()
	slow := &gatedSnapshotStore{SnapshotStore: base, entered: make(chan domain.Snapshot, 1), release: make(chan struct{})}
	worker := NewSnapshotWorker(slow)
	worker.Offer(emptySnapshot(1))
	done := make(chan error, 1)
	go func() {
		_, err := worker.RunOnce(context.Background())
		done <- err
	}()
	select {
	case <-slow.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("active Save did not start")
	}
	worker.Offer(emptySnapshot(2))
	worker.Offer(emptySnapshot(3))
	worker.Offer(emptySnapshot(2))
	close(slow.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	saved, err := worker.RunOnce(context.Background())
	if err != nil || !saved {
		t.Fatalf("RunOnce() pending = %v, %v; want latest pending candidate", saved, err)
	}
	snapshot, found, err := base.LoadLatest(context.Background())
	if err != nil || !found || snapshot.LastSequence != 3 {
		t.Fatalf("saved snapshot = seq %d, found %v, err %v; want sequence 3", snapshot.LastSequence, found, err)
	}
}

func TestLateOldSnapshot(t *testing.T) {
	store := memory.NewSnapshotStore()
	worker := NewSnapshotWorker(store)
	worker.Offer(emptySnapshot(2))
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("RunOnce(newer) = %v, %v", worked, err)
	}
	worker.Offer(emptySnapshot(1))
	if worked, err := worker.RunOnce(context.Background()); err != nil || worked {
		t.Fatalf("RunOnce(late older) = %v, %v; want no work", worked, err)
	}
	snapshot, found, err := store.LoadLatest(context.Background())
	if err != nil || !found || snapshot.LastSequence != 2 {
		t.Fatalf("LoadLatest() sequence = %d, found %v, err %v; want 2", snapshot.LastSequence, found, err)
	}
}

func TestRetryWithoutNewEvents(t *testing.T) {
	base := memory.NewSnapshotStore()
	flaky := &failOnceSnapshotStore{SnapshotStore: base}
	worker := NewSnapshotWorker(flaky)
	worker.Offer(emptySnapshot(3))
	if worked, err := worker.RunOnce(context.Background()); err == nil || !worked {
		t.Fatalf("first RunOnce() = %v, %v; want failed save attempt", worked, err)
	}
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("retry RunOnce() = %v, %v; want retry without a new offer", worked, err)
	}
	snapshot, found, err := base.LoadLatest(context.Background())
	if err != nil || !found || snapshot.LastSequence != 3 {
		t.Fatalf("LoadLatest() sequence = %d, found %v, err %v; want 3", snapshot.LastSequence, found, err)
	}
}

func TestWorkerCancellation(t *testing.T) {
	base := memory.NewSnapshotStore()
	slow := &gatedSnapshotStore{SnapshotStore: base, entered: make(chan domain.Snapshot, 1), release: make(chan struct{})}
	worker := NewSnapshotWorker(slow)
	worker.Offer(emptySnapshot(1))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-slow.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("active Save did not start")
	}
	worker.Offer(emptySnapshot(2))
	cancel()
	close(slow.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v; want context.Canceled", err)
	}
	if _, found, err := base.LoadLatest(context.Background()); err != nil || found {
		t.Fatalf("queued work was saved after cancellation: found %v, err %v", found, err)
	}
}

type gatedSnapshotStore struct {
	SnapshotStore
	entered chan domain.Snapshot
	release chan struct{}
	once    sync.Once
}

func (s *gatedSnapshotStore) Save(ctx context.Context, snapshot domain.Snapshot) error {
	s.once.Do(func() { s.entered <- domain.CloneSnapshot(snapshot) })
	<-s.release
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.SnapshotStore.Save(ctx, snapshot)
}

type failOnceSnapshotStore struct {
	SnapshotStore
	mu     sync.Mutex
	failed bool
}

func (s *failOnceSnapshotStore) Save(ctx context.Context, snapshot domain.Snapshot) error {
	s.mu.Lock()
	if !s.failed {
		s.failed = true
		s.mu.Unlock()
		return errors.New("save failed")
	}
	s.mu.Unlock()
	return s.SnapshotStore.Save(ctx, snapshot)
}

func emptySnapshot(sequence int64) domain.Snapshot {
	product := domain.Product{ProductID: "p", Name: "Product", BaseUnit: domain.BaseUnitG}
	record := domain.CommandRecord{CommandID: "c", CommandType: "RegisterProduct", CommandFingerprint: "fingerprint", Result: domain.CommandResult{Kind: domain.CommandResultProductRegistered, Product: &product}}
	return domain.Snapshot{SchemaVersion: domain.InventorySchemaV1, StreamID: domain.InventoryStreamID, LastSequence: sequence, State: domain.InventoryState{Products: map[string]domain.Product{"p": product}, Dishes: map[string]domain.Dish{}, Deliveries: map[string]domain.Delivery{}, Batches: map[string]domain.Batch{}, Orders: map[string]domain.Order{}, OrdersByProposal: map[string]string{}, Commands: map[string]domain.CommandRecord{"c": record}, StateVersion: sequence}}
}
