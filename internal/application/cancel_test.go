package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

// versionFaultStore fails Version once with err while armed.
type versionFaultStore struct {
	EventStore
	mu  sync.Mutex
	err error
}

func (s *versionFaultStore) arm(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *versionFaultStore) Version(ctx context.Context) (int64, error) {
	s.mu.Lock()
	err := s.err
	s.err = nil
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return s.EventStore.Version(ctx)
}

// A request that gives up while waiting for the owner leaves the projection as
// it was: the next request does not rebuild it from the journal.
func TestCanceledWaiterKeepsProjection(t *testing.T) {
	at := moscowTime(2026, time.April, 1, 10)
	owner, log, _ := recoveredOwner(t, at)
	owner.mu.lockUncancellable()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := owner.GetInventory(ctx)
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, contract.ErrInventoryUnavailable) {
			t.Fatalf("GetInventory() = %v; want the cancellation as InventoryUnavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a canceled waiter kept waiting for the owner")
	}
	owner.mu.Unlock()
	if _, err := owner.GetInventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !owner.usable || log.journalReads() != 0 {
		t.Fatalf("usable %v after %d journal walks; want a usable projection and no walk", owner.usable, log.journalReads())
	}
}

func TestWaiterLeavesOnDeadline(t *testing.T) {
	at := moscowTime(2026, time.April, 1, 10)
	owner, _, _ := recoveredOwner(t, at)
	owner.mu.lockUncancellable()
	defer owner.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := owner.CommitAcceptedOrder(ctx, commitDish("c1", "p1"))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
		t.Fatalf("CommitAcceptedOrder() = %v after %v; want the deadline soon", err, time.Since(started))
	}
	if _, found, err := owner.events.FindCommand(context.Background(), "c1"); err != nil || found {
		t.Fatalf("a waiter that left wrote a command: found %v, %v", found, err)
	}
}

func TestCanceledVersionReadKeepsProjection(t *testing.T) {
	at := moscowTime(2026, time.April, 1, 10)
	log := &readLog{EventStore: seedStore(t, at.Add(72*time.Hour), 20000, 0)}
	store := &versionFaultStore{EventStore: log}
	owner := w3Owner(t, store, &testClock{now: at})
	if err := owner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	log.reset()
	for _, fault := range []error{context.Canceled, context.DeadlineExceeded} {
		store.arm(fault)
		if _, err := owner.GetInventory(context.Background()); !errors.Is(err, fault) {
			t.Fatalf("GetInventory() = %v; want %v", err, fault)
		}
		if !owner.usable {
			t.Fatalf("%v made a consistent projection unusable", fault)
		}
	}
	if _, err := owner.GetInventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reads := log.journalReads(); reads != 0 {
		t.Fatalf("the next request walked the journal %d times; want 0", reads)
	}
}

func TestCanceledRecoveryKeepsPreviousProjection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	at := moscowTime(2026, time.April, 1, 10)
	counting := &journalCountingStore{EventStore: seedStore(t, at.Add(72*time.Hour), 20000, 0)}
	owner := w3Owner(t, counting, &testClock{now: at})
	if err := owner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := domain.CloneInventoryState(owner.state)
	counting.afterBatch = cancel
	owner.mu.lockUncancellable()
	err := owner.recoverLocked(ctx)
	usable := owner.usable
	owner.mu.Unlock()
	if !errors.Is(err, context.Canceled) || !usable || !domain.EqualInventoryState(before, owner.state) {
		t.Fatalf("recoverLocked() = %v, usable %v; want the cancellation with the previous projection kept", err, usable)
	}
}
