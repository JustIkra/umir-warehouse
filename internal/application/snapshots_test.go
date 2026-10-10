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
	store := storeWithCompoundDeliveryExpiry(t)
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
	if snapshot.LastSequence != 4 || snapshot.LastSequence != snapshot.State.StateVersion {
		t.Fatalf("snapshot sequence/state version = %d/%d; want final sequence 4 of compound command", snapshot.LastSequence, snapshot.State.StateVersion)
	}
	owner.mu.Lock()
	lastSaved := owner.lastSavedSequence
	owner.mu.Unlock()
	if lastSaved != 4 {
		t.Fatalf("confirmed saved sequence = %d; want monotonic acknowledgment 4", lastSaved)
	}
	batches, err := store.ReadBatches(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if batches[1].FinalSequence != 4 || len(batches[1].Events) != 3 {
		t.Fatalf("delivery command = %d events ending at %d; want one three-event compound command", len(batches[1].Events), batches[1].FinalSequence)
	}
	inside := domain.CloneSnapshot(snapshot)
	inside.LastSequence = 3
	inside.State.StateVersion = 3
	if err := auditSnapshotPrefix(inside, batches); err == nil {
		t.Fatal("snapshot candidate at sequence 3 inside the compound command was accepted")
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

func storeWithCompoundDeliveryExpiry(t *testing.T) *memory.EventStore {
	t.Helper()
	ctx := context.Background()
	store := memory.NewEventStore()
	registeredAt := moscowTime(2026, time.March, 1, 12)
	appendFixture(t, ctx, store, "register-product", "RegisterProduct", domain.Event{EventID: "product-event", RecordedAt: registeredAt, Type: domain.EventProductRegistered, Payload: domain.EventPayload{Product: &domain.Product{ProductID: "product", Name: "Product", BaseUnit: domain.BaseUnitG}}})
	manufacturedAt := moscowTime(2026, time.February, 28, 12)
	arrivalAt := moscowTime(2026, time.March, 1, 12)
	processedAt := moscowTime(2026, time.March, 3, 12)
	expiresAt := moscowTime(2026, time.March, 2, 12)
	delivery := domain.Delivery{DeliveryID: "delivery", Number: "invoice", CreatedAt: arrivalAt, Lines: []domain.DeliveryLine{
		{ProductID: "product", ManufacturedAt: manufacturedAt, ShelfLife: domain.ShelfLife{Value: 2, Unit: domain.ShelfLifeDay}, ReceivedAt: arrivalAt, Quantity: 1000},
		{ProductID: "product", ManufacturedAt: manufacturedAt, ShelfLife: domain.ShelfLife{Value: 2, Unit: domain.ShelfLifeDay}, ReceivedAt: arrivalAt, Quantity: 2000},
	}}
	batches := []domain.Batch{
		{BatchID: "batch-1", DeliveryID: "delivery", LineNo: 1, ProductID: "product", ManufacturedAt: manufacturedAt, ShelfLife: domain.ShelfLife{Value: 2, Unit: domain.ShelfLifeDay}, ReceivedAt: arrivalAt, ExpiresAt: expiresAt, ReceivedQuantity: 1000, RemainingQuantity: 1000},
		{BatchID: "batch-2", DeliveryID: "delivery", LineNo: 2, ProductID: "product", ManufacturedAt: manufacturedAt, ShelfLife: domain.ShelfLife{Value: 2, Unit: domain.ShelfLifeDay}, ReceivedAt: arrivalAt, ExpiresAt: expiresAt, ReceivedQuantity: 2000, RemainingQuantity: 2000},
	}
	fingerprint := domain.Fingerprint("ReceiveDelivery", []byte("delivery"))
	events := []domain.Event{
		{EventID: "delivery-event", RecordedAt: processedAt, Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{Delivery: &delivery, Batches: batches}},
		{EventID: "expiry-event-1", RecordedAt: processedAt, Type: domain.EventBatchExpired, Payload: domain.EventPayload{Expiry: &domain.ExpiryResult{BatchID: "batch-1", ProductID: "product", WrittenOffQuantity: 1000, Reason: domain.ExpiryReasonExpiration, ExpiresAt: expiresAt, ProcessedAt: processedAt}}},
		{EventID: "expiry-event-2", RecordedAt: processedAt, Type: domain.EventBatchExpired, Payload: domain.EventPayload{Expiry: &domain.ExpiryResult{BatchID: "batch-2", ProductID: "product", WrittenOffQuantity: 2000, Reason: domain.ExpiryReasonExpiration, ExpiresAt: expiresAt, ProcessedAt: processedAt}}},
	}
	if _, err := store.AppendBatch(ctx, domain.CommandBatch{ExpectedVersion: 1, CommandID: "receive-delivery", CommandType: "ReceiveDelivery", CommandFingerprint: fingerprint, Events: events}); err != nil {
		t.Fatal(err)
	}
	return store
}
