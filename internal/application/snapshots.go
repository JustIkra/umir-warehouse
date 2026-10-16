package application

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	verify         func(context.Context, domain.Snapshot) error
	// rejectedSequence is the highest position a candidate was dropped at
	// because it contradicted the journal; candidates up to it are not judged again.
	rejectedSequence int64
	logf             func(string, ...any)
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

func (e *SnapshotSaveError) RetryableSnapshotSave() {}

func NewSnapshotWorker(store SnapshotStore) *SnapshotWorker {
	return &SnapshotWorker{store: store, notify: make(chan struct{}, 1), logf: log.Printf}
}

func (w *SnapshotWorker) Offer(snapshot domain.Snapshot) {
	w.mu.Lock()
	if snapshot.LastSequence <= w.savedSequence || snapshot.LastSequence <= w.rejectedSequence || w.active && snapshot.LastSequence <= w.activeSequence || w.pending != nil && snapshot.LastSequence <= w.pending.LastSequence {
		w.mu.Unlock()
		return
	}
	// The one copy: the worker owns the pending snapshot from here on, and the
	// caller's state may change as soon as Offer returns.
	candidate := domain.CloneSnapshot(snapshot)
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
	// Nothing else holds or changes the pending snapshot, so it is used as is.
	candidate := *w.pending
	verify := w.verify
	w.pending = nil
	w.active = true
	w.activeSequence = candidate.LastSequence
	w.mu.Unlock()

	var verifyErr error
	if verify != nil {
		verifyErr = verify(ctx, candidate)
	}
	err := verifyErr
	if err == nil {
		err = w.store.Save(ctx, candidate)
	}
	// A candidate that contradicts the journal is dropped, not retried, and its
	// position is not judged again: the owner offers a newer one with its next
	// write. The worker goes on, so one wrong candidate does not stop snapshots.
	rejected := errors.Is(verifyErr, domain.ErrInvalidSnapshot)
	w.mu.Lock()
	w.active = false
	w.activeSequence = 0
	acknowledge := w.onSaved
	logf := w.logf
	if rejected {
		w.rejectedSequence = max(w.rejectedSequence, candidate.LastSequence)
		if w.pending != nil && w.pending.LastSequence <= w.rejectedSequence {
			w.pending = nil
		}
	}
	if err != nil && !rejected {
		if w.pending == nil || w.pending.LastSequence < candidate.LastSequence {
			w.pending = &candidate
		}
	} else if err == nil && candidate.LastSequence > w.savedSequence {
		w.savedSequence = candidate.LastSequence
	}
	more := w.pending != nil
	w.mu.Unlock()
	if err == nil && acknowledge != nil {
		acknowledge(candidate.LastSequence)
	}
	if rejected {
		logf("warehouse: snapshot not saved: %v", verifyErr)
		err = nil
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

func (w *SnapshotWorker) setLogger(logf func(string, ...any)) {
	w.mu.Lock()
	w.logf = logf
	w.mu.Unlock()
}

func (w *SnapshotWorker) setOnSaved(callback func(int64)) {
	w.mu.Lock()
	w.onSaved = callback
	w.mu.Unlock()
}

func (w *SnapshotWorker) setVerifier(verify func(context.Context, domain.Snapshot) error) {
	w.mu.Lock()
	w.verify = verify
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
	o.mu.lockUncancellable()
	defer o.mu.Unlock()
	if store == nil || eventsPerSnapshot <= 0 || o.started {
		return contract.ErrInvalidArgument
	}
	o.snapshotStore = store
	o.snapshotEvery = eventsPerSnapshot
	o.snapshotWorker = NewSnapshotWorker(store)
	o.snapshotWorker.setLogger(o.logf)
	o.snapshotWorker.setOnSaved(o.confirmSnapshotSaved)
	events := o.events
	o.snapshotWorker.setVerifier(func(ctx context.Context, candidate domain.Snapshot) error {
		return verifySnapshot(ctx, events, store, candidate)
	})
	return nil
}

var errCandidateReached = errors.New("candidate sequence reached")

// verifySnapshot checks a candidate against the journal once, before it is
// saved and outside the owner's lock: the previous valid snapshot plus the
// journal up to the candidate must give the candidate's state. A previous
// snapshot that cannot show this may itself be wrong, so the candidate is then
// judged from the start of the journal.
func verifySnapshot(ctx context.Context, events EventStore, snapshots SnapshotStore, candidate domain.Snapshot) error {
	previous, found, err := snapshots.LoadLatest(ctx)
	if err != nil {
		return err
	}
	if found && previous.LastSequence <= candidate.LastSequence && domain.ValidateSnapshot(previous) == nil {
		// The store returns a copy, so replaying onto it is safe.
		err := replayToCandidate(ctx, events, previous.State, candidate)
		if err == nil || isContextErr(err) {
			return err
		}
	}
	return replayToCandidate(ctx, events, domain.InventoryState{}, candidate)
}

func replayToCandidate(ctx context.Context, events EventStore, state domain.InventoryState, candidate domain.Snapshot) error {
	target := candidate.LastSequence
	if state.StateVersion < target {
		err := events.ScanBatches(ctx, state.StateVersion, func(batch domain.RecordedBatch) error {
			if err := state.ApplyBatch(batch); err != nil {
				return err
			}
			if state.StateVersion >= target {
				return errCandidateReached
			}
			return nil
		})
		if err != nil && !errors.Is(err, errCandidateReached) {
			return err
		}
	}
	if state.StateVersion != target || !domain.EqualInventoryState(state, candidate.State) {
		return fmt.Errorf("%w: snapshot at sequence %d does not match the journal", domain.ErrInvalidSnapshot, target)
	}
	return nil
}

func (o *Owner) RunSnapshotOnce(ctx context.Context) (bool, error) {
	if err := o.mu.Lock(ctx); err != nil {
		return false, err
	}
	worker := o.snapshotWorker
	o.mu.Unlock()
	if worker == nil {
		return false, contract.ErrInvalidArgument
	}
	return worker.RunOnce(ctx)
}

func (o *Owner) RunSnapshots(ctx context.Context) error {
	if err := o.mu.Lock(ctx); err != nil {
		return err
	}
	worker := o.snapshotWorker
	o.mu.Unlock()
	if worker == nil {
		return contract.ErrInvalidArgument
	}
	return worker.Run(ctx)
}

func (o *Owner) confirmSnapshotSaved(sequence int64) {
	o.mu.lockUncancellable()
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
		SchemaVersion: domain.SnapshotSchemaV2,
		StreamID:      domain.InventoryStreamID,
		LastSequence:  o.state.StateVersion,
		State:         o.state,
	}
	// Offer copies the snapshot before returning, while the lock is held.
	o.snapshotWorker.Offer(snapshot)
}
