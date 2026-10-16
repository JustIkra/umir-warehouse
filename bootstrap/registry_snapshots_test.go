package bootstrap

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/application"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

// flakySnapshots fails Save while failing is set and counts the saves that went through.
type flakySnapshots struct {
	application.SnapshotStore
	mu      sync.Mutex
	failing bool
	saved   int
}

func (s *flakySnapshots) Save(ctx context.Context, snapshot domain.Snapshot) error {
	s.mu.Lock()
	failing := s.failing
	s.mu.Unlock()
	if failing {
		return errors.New("snapshot store unavailable")
	}
	if err := s.SnapshotStore.Save(ctx, snapshot); err != nil {
		return err
	}
	s.mu.Lock()
	s.saved++
	s.mu.Unlock()
	return nil
}

func (s *flakySnapshots) set(failing bool) {
	s.mu.Lock()
	s.failing = failing
	s.mu.Unlock()
}

func (s *flakySnapshots) savedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved
}

// One restaurant whose snapshots cannot be saved does not stop the snapshot
// workers of the others, and its own worker tries again.
func TestSnapshotSaveFailureIsolatedPerRestaurant(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const other = "33333333-3333-4333-8333-333333333334"
	stores := map[string]*flakySnapshots{}
	var mu sync.Mutex
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	registry, err := NewRegistry(Options{
		Now: func() time.Time { return now }, NewID: func() string { return "id" }, EventsPerSnapshot: 1,
		WrapSnapshotStore: func(scope contract.Scope, store application.SnapshotStore) application.SnapshotStore {
			wrapped := &flakySnapshots{SnapshotStore: store}
			mu.Lock()
			stores[scope.RestaurantID] = wrapped
			mu.Unlock()
			return wrapped
		},
	}, settingsEpoch)
	if err != nil {
		t.Fatal(err)
	}
	registry.snapshotRetry = 5 * time.Millisecond
	broken, healthy := settingsScope("Europe/Moscow", "1"), settingsScope("Europe/Moscow", "1")
	healthy.RestaurantID = other
	for _, scope := range []contract.Scope{broken, healthy} {
		if err := registry.Register(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	brokenStore, healthyStore := stores[broken.RestaurantID], stores[healthy.RestaurantID]
	mu.Unlock()
	brokenStore.set(true)

	finished := make(chan error, 1)
	go func() { finished <- registry.RunSnapshotWorker(ctx) }()
	write := func(scope contract.Scope, id string) {
		t.Helper()
		runtime, err := registry.Resolve(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: id, ProductID: id, Name: id, BaseUnit: contract.BaseUnitG}); err != nil {
			t.Fatal(err)
		}
	}
	write(broken, "a")
	write(healthy, "b")
	deadline := time.Now().Add(10 * time.Second)
	for healthyStore.savedCount() == 0 {
		select {
		case err := <-finished:
			t.Fatalf("the snapshot worker stopped because one restaurant could not save: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the healthy restaurant's snapshot was never saved")
		}
		time.Sleep(time.Millisecond)
	}
	brokenStore.set(false)
	for brokenStore.savedCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the failing restaurant's worker never tried again")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunSnapshotWorker() = %v; want the cancellation", err)
	}
}
