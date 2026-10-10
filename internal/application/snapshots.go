package application

import (
	"context"
	"fmt"
	"sync"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type SnapshotWorker struct {
	mu             sync.Mutex
	store          SnapshotStore
	pending        *domain.Snapshot
	active         bool
	activeSequence int64
	savedSequence  int64
	notify         chan struct{}
	onSaved        func(int64)
}

// SnapshotSaveError identifies a failed SnapshotStore save and preserves its cause.
type SnapshotSaveError struct {
	Err error
}

func (e *SnapshotSaveError) Error() string {
	return fmt.Sprintf("save snapshot: %v", e.Err)
}

func (e *SnapshotSaveError) Unwrap() error {
	return e.Err
}

func NewSnapshotWorker(store SnapshotStore) *SnapshotWorker {
	return &SnapshotWorker{store: store, notify: make(chan struct{}, 1)}
}

func (w *SnapshotWorker) Offer(snapshot domain.Snapshot) {
	candidate := domain.CloneSnapshot(snapshot)
	w.mu.Lock()
	if candidate.LastSequence <= w.savedSequence || w.active && candidate.LastSequence <= w.activeSequence || w.pending != nil && candidate.LastSequence <= w.pending.LastSequence {
		w.mu.Unlock()
		return
	}
	w.pending = &candidate
	w.mu.Unlock()
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *SnapshotWorker) RunOnce(ctx context.Context) (bool, error) {
	if w.store == nil {
		return false, contract.ErrInvalidArgument
	}
	w.mu.Lock()
	if w.active || w.pending == nil {
		w.mu.Unlock()
		return false, nil
	}
	candidate := domain.CloneSnapshot(*w.pending)
	w.pending = nil
	w.active = true
	w.activeSequence = candidate.LastSequence
	w.mu.Unlock()

	err := w.store.Save(ctx, candidate)
	w.mu.Lock()
	w.active = false
	w.activeSequence = 0
	acknowledge := w.onSaved
	if err != nil {
		if w.pending == nil || w.pending.LastSequence < candidate.LastSequence {
			w.pending = &candidate
		}
	} else if candidate.LastSequence > w.savedSequence {
		w.savedSequence = candidate.LastSequence
	}
	more := w.pending != nil
	w.mu.Unlock()
	if err == nil && acknowledge != nil {
		acknowledge(candidate.LastSequence)
	}
	if more {
		select {
		case w.notify <- struct{}{}:
		default:
		}
	}
	return true, err
}

func (w *SnapshotWorker) Run(ctx context.Context) error {
	defer func() {
		if ctx.Err() != nil {
			w.mu.Lock()
			w.pending = nil
			w.mu.Unlock()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		worked, err := w.RunOnce(ctx)
		if err != nil {
			if worked {
				return &SnapshotSaveError{Err: err}
			}
			return err
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.notify:
		}
	}
}

func (w *SnapshotWorker) setOnSaved(callback func(int64)) {
	w.mu.Lock()
	w.onSaved = callback
	w.mu.Unlock()
}

func (w *SnapshotWorker) observeSaved(sequence int64) {
	w.mu.Lock()
	if sequence > w.savedSequence {
		w.savedSequence = sequence
	}
	if w.pending != nil && w.pending.LastSequence <= w.savedSequence {
		w.pending = nil
	}
	w.mu.Unlock()
}

func (o *Owner) ConfigureSnapshots(store SnapshotStore, eventsPerSnapshot int64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if store == nil || eventsPerSnapshot <= 0 || o.started {
		return contract.ErrInvalidArgument
	}
	o.snapshotStore = store
	o.snapshotEvery = eventsPerSnapshot
	o.snapshotWorker = NewSnapshotWorker(store)
	o.snapshotWorker.setOnSaved(o.confirmSnapshotSaved)
	return nil
}

func (o *Owner) RunSnapshotOnce(ctx context.Context) (bool, error) {
	o.mu.Lock()
	worker := o.snapshotWorker
	o.mu.Unlock()
	if worker == nil {
		return false, contract.ErrInvalidArgument
	}
	return worker.RunOnce(ctx)
}

func (o *Owner) RunSnapshots(ctx context.Context) error {
	o.mu.Lock()
	worker := o.snapshotWorker
	o.mu.Unlock()
	if worker == nil {
		return contract.ErrInvalidArgument
	}
	return worker.Run(ctx)
}

func (o *Owner) confirmSnapshotSaved(sequence int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if sequence > o.lastSavedSequence {
		o.lastSavedSequence = sequence
	}
}

func (o *Owner) offerSnapshotLocked() {
	if o.snapshotWorker == nil || o.state.StateVersion < o.lastSavedSequence || o.state.StateVersion-o.lastSavedSequence < o.snapshotEvery {
		return
	}
	snapshot := domain.Snapshot{
		SchemaVersion: domain.InventorySchemaV1,
		StreamID:      domain.InventoryStreamID,
		LastSequence:  o.state.StateVersion,
		State:         domain.CloneInventoryState(o.state),
	}
	o.snapshotWorker.Offer(snapshot)
}
