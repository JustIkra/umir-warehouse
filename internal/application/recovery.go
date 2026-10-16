package application

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func (o *Owner) syncProjectionLocked(ctx context.Context) error {
	o.started = true
	if o.pending != nil {
		_, _, err := o.resolvePendingLocked(ctx)
		if err != nil {
			if errors.Is(err, domain.ErrConcurrentModification) {
				return o.recoverCurrentLocked(ctx)
			}
			return err
		}
	}
	if !o.usable {
		return o.recoverCurrentLocked(ctx)
	}
	version, err := o.events.Version(ctx)
	if err != nil {
		o.markUnusable(err)
		return err
	}
	if version < o.state.StateVersion {
		o.usable = false
		return o.recoverCurrentLocked(ctx)
	}
	if version == o.state.StateVersion {
		return nil
	}
	// Every batch applies atomically, so a scan cut short by the context leaves
	// a consistent prefix of the journal in the projection.
	var applyErr error
	err = o.events.ScanBatches(ctx, o.state.StateVersion, func(batch domain.RecordedBatch) error {
		applyErr = o.applyBatchLocked(batch)
		return applyErr
	})
	switch {
	case applyErr != nil:
		o.usable = false
		return o.recoverCurrentLocked(ctx)
	case err != nil && isContextErr(err):
		return err
	case err != nil:
		o.usable = false
		return err
	case o.state.StateVersion < version:
		o.usable = false
		return o.recoverCurrentLocked(ctx)
	}
	o.publishProjection()
	return nil
}

func (o *Owner) recoverCurrentLocked(ctx context.Context) error {
	for range 3 {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := o.recoverLocked(ctx)
		if !errors.Is(err, domain.ErrConcurrentModification) {
			return err
		}
	}
	return domain.ErrConcurrentModification
}

// projection is a recovery candidate built outside the Owner's fields.
type projection struct {
	state       domain.InventoryState
	expirations map[string]time.Time
	// snapshotSequence is the snapshot the candidate started from; zero when
	// it was replayed from the start of the journal.
	snapshotSequence int64
}

// recoverLocked rebuilds the projection and publishes it only if the whole
// rebuild succeeds; on any error the previous projection stays. It stays
// usable when the rebuild fails for a reason outside the journal (a canceled
// context, an unavailable store) and stops being usable when the journal
// itself is inconsistent.
func (o *Owner) recoverLocked(ctx context.Context) error {
	o.started = true
	// The pending command is looked up first: a failing lookup or a conflicting
	// record is known before the journal is read.
	committed, err := o.pendingCommittedLocked(ctx)
	if err != nil {
		return err
	}
	candidate, err := o.buildProjection(ctx)
	if err != nil {
		if isJournalFault(err) {
			o.usable = false
		}
		return err
	}
	if o.pending != nil && !committed {
		// Another writer may have recorded the command while the journal was read.
		if committed, err = o.pendingCommittedLocked(ctx); err != nil {
			return err
		}
	}
	if committed {
		o.pending = nil
	}
	o.state, o.expirations = candidate.state, candidate.expirations
	if candidate.snapshotSequence > 0 {
		o.lastSavedSequence = max(o.lastSavedSequence, candidate.snapshotSequence)
		o.snapshotWorker.observeSaved(candidate.snapshotSequence)
	}
	o.publishProjection()
	return nil
}

// pendingCommittedLocked reports whether the store already holds the pending
// command. A record with another identity is a conflict.
func (o *Owner) pendingCommittedLocked(ctx context.Context) (bool, error) {
	if o.pending == nil {
		return false, nil
	}
	record, found, err := o.events.FindCommand(ctx, o.pending.CommandID)
	if err != nil || !found {
		return false, err
	}
	if record.CommandType != o.pending.CommandType || record.CommandFingerprint != o.pending.CommandFingerprint {
		o.usable = false
		return false, domain.ErrCommandIdentityConflict
	}
	return true, nil
}

// buildProjection prefers the latest snapshot plus the journal tail and falls
// back to the whole journal when the snapshot is invalid or contradicts it.
// Only a fault in the snapshot or the journal causes the fallback; any other
// error, such as a store failure, is returned.
func (o *Owner) buildProjection(ctx context.Context) (projection, error) {
	if o.snapshotStore != nil {
		snapshot, found, err := o.snapshotStore.LoadLatest(ctx)
		if err != nil && !isJournalFault(err) {
			return projection{}, err
		}
		if err != nil {
			o.logf("warehouse: the latest snapshot is unusable, replaying the journal from the start: %v", err)
		} else if found {
			candidate, err := o.projectFromSnapshot(ctx, snapshot)
			if err == nil || !isJournalFault(err) {
				return candidate, err
			}
			o.logf("warehouse: snapshot at sequence %d is unusable, replaying the journal from the start: %v", snapshot.LastSequence, err)
		}
	}
	return o.projectFromJournal(ctx)
}

// isJournalFault reports whether err says that a snapshot or the journal is
// itself wrong, as opposed to the store being unavailable, the context ending
// or another writer being ahead.
func isJournalFault(err error) bool {
	for _, fault := range []error{
		domain.ErrInvalidSnapshot, domain.ErrUnsupportedSchema, domain.ErrInvalidEventBatch, domain.ErrInvalidEvent,
		domain.ErrInvalidRecordedBatch, domain.ErrProductNotFound, domain.ErrDishNotFound, domain.ErrBatchNotFound,
		domain.ErrInsufficientQuantity, domain.ErrQuantityOverflow, domain.ErrInvalidQuantity,
		domain.ErrInvalidShelfLife, domain.ErrInvalidBusinessTime, domain.ErrShelfLifeOverflow,
	} {
		if errors.Is(err, fault) {
			return true
		}
	}
	return false
}

// projectFromSnapshot takes ownership of the snapshot state: stores return copies.
func (o *Owner) projectFromSnapshot(ctx context.Context, snapshot domain.Snapshot) (projection, error) {
	if err := domain.ValidateSnapshot(snapshot); err != nil {
		return projection{}, err
	}
	// The state may come from storage without the owner's derived batch index.
	snapshot.State.Reindex()
	index, err := expirationIndex(snapshot.State)
	if err != nil {
		return projection{}, err
	}
	candidate := projection{state: snapshot.State, expirations: index, snapshotSequence: snapshot.LastSequence}
	return o.completeProjection(ctx, candidate)
}

func (o *Owner) projectFromJournal(ctx context.Context) (projection, error) {
	return o.completeProjection(ctx, projection{expirations: map[string]time.Time{}})
}

// completeProjection replays the journal after the candidate's version and
// checks the result against the store's version: a newer store means a
// concurrent writer (the caller retries), an older one an inconsistent journal.
func (o *Owner) completeProjection(ctx context.Context, candidate projection) (projection, error) {
	walk := newJournalWalk(candidate.state.StateVersion)
	err := o.events.ScanBatches(ctx, candidate.state.StateVersion, func(batch domain.RecordedBatch) error {
		if err := walk.check(batch); err != nil {
			return err
		}
		return applyBatch(&candidate.state, candidate.expirations, batch)
	})
	if err != nil {
		return projection{}, err
	}
	version, err := o.events.Version(ctx)
	if err != nil {
		return projection{}, err
	}
	switch {
	case version > candidate.state.StateVersion:
		return projection{}, domain.ErrConcurrentModification
	case version < candidate.state.StateVersion:
		return projection{}, domain.ErrInvalidRecordedBatch
	}
	return candidate, nil
}

// markUnusable stops the projection from being served after err, unless err is
// the end of a request's context, which says nothing about the journal.
func (o *Owner) markUnusable(err error) {
	if !isContextErr(err) {
		o.usable = false
	}
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (o *Owner) publishProjection() {
	o.usable = true
	o.offerSnapshotLocked()
}

// journalWalk checks a journal read as a stream: the sequence is gapless from
// where the walk starts and every batch is well formed. Command and event IDs
// are unique across the journal, which only a walk from the start can see; a
// walk from a snapshot relies on the store's own uniqueness checks.
type journalWalk struct {
	next     int64
	commands map[string]struct{}
	eventIDs map[string]struct{}
}

func newJournalWalk(after int64) *journalWalk {
	walk := &journalWalk{next: after + 1}
	if after == 0 {
		walk.commands = map[string]struct{}{}
		walk.eventIDs = map[string]struct{}{}
	}
	return walk
}

func (w *journalWalk) check(batch domain.RecordedBatch) error {
	if len(batch.Events) == 0 || batch.CommandID == "" || batch.CommandType == "" || batch.CommandFingerprint == "" || batch.FinalSequence != batch.Events[len(batch.Events)-1].Sequence {
		return domain.ErrInvalidRecordedBatch
	}
	if w.commands != nil {
		if _, exists := w.commands[batch.CommandID]; exists {
			return domain.ErrInvalidRecordedBatch
		}
		w.commands[batch.CommandID] = struct{}{}
	}
	if int64(len(batch.Events)) > math.MaxInt64-w.next {
		return domain.ErrInvalidRecordedBatch
	}
	for _, event := range batch.Events {
		if event.EventID == "" {
			return domain.ErrInvalidRecordedBatch
		}
		if w.eventIDs != nil {
			if _, exists := w.eventIDs[event.EventID]; exists {
				return domain.ErrInvalidRecordedBatch
			}
			w.eventIDs[event.EventID] = struct{}{}
		}
		if err := domain.ValidateRecordedEvent(event, batch, w.next); err != nil {
			return err
		}
		w.next++
	}
	_, err := domain.ResultForBatch(batch)
	return err
}

func expirationIndex(state domain.InventoryState) (map[string]time.Time, error) {
	index := make(map[string]time.Time)
	for batchID, batch := range state.Batches {
		if batchID == "" || batch.BatchID != batchID || batch.RemainingQuantity < 0 {
			return nil, domain.ErrInvalidRecordedBatch
		}
		if batch.RemainingQuantity > 0 && !batch.PendingWriteoff {
			index[batchID] = batch.ExpiresAt
		}
	}
	return index, nil
}

// applyBatch applies one recorded batch to the state and the expiration index
// in place. The batch is planned first, so a rejected batch changes nothing.
func applyBatch(state *domain.InventoryState, expirations map[string]time.Time, batch domain.RecordedBatch) error {
	delta, err := domain.PlanBatch(state, batch)
	if err != nil {
		return err
	}
	state.Merge(delta)
	for batchID, b := range delta.Batches {
		if b.RemainingQuantity > 0 && !b.PendingWriteoff {
			expirations[batchID] = b.ExpiresAt
		} else {
			delete(expirations, batchID)
		}
	}
	return nil
}

func (o *Owner) applyBatchLocked(batch domain.RecordedBatch) error {
	if o.expirations == nil {
		o.expirations = map[string]time.Time{}
	}
	return applyBatch(&o.state, o.expirations, batch)
}

func (o *Owner) applyCommittedBatch(batch domain.RecordedBatch) error {
	if err := o.applyBatchLocked(batch); err != nil {
		return err
	}
	o.publishProjection()
	return nil
}

func (o *Owner) resolvePendingLocked(ctx context.Context) (domain.CommandRecord, bool, error) {
	if o.pending == nil {
		return domain.CommandRecord{}, false, nil
	}
	pending := cloneCommandBatch(*o.pending)
	record, found, err := o.events.FindCommand(ctx, pending.CommandID)
	if err != nil {
		o.markUnusable(err)
		return domain.CommandRecord{}, false, err
	}
	if found {
		if record.CommandType != pending.CommandType || record.CommandFingerprint != pending.CommandFingerprint {
			o.usable = false
			o.pending = nil
			return record, false, domain.ErrCommandIdentityConflict
		}
		batches, err := o.events.ReadBatches(ctx, pending.ExpectedVersion)
		if err != nil {
			o.markUnusable(err)
			o.pending = nil
			return record, true, err
		}
		for _, batch := range batches {
			if batch.CommandID == pending.CommandID {
				if err := o.applyCommittedBatch(batch); err != nil {
					o.usable = false
					o.pending = nil
					return record, true, err
				}
				o.pending = nil
				return record, true, nil
			}
		}
		o.usable = false
		o.pending = nil
		return record, true, domain.ErrInvalidRecordedBatch
	}
	result, err := o.events.AppendBatch(ctx, *pending)
	if err != nil {
		o.markUnusable(err)
		if errors.Is(err, domain.ErrConcurrentModification) {
			o.pending = nil
		}
		return domain.CommandRecord{}, false, err
	}
	if err := o.applyCommittedBatch(result.Batch); err != nil {
		o.usable = false
		o.pending = nil
		return result.Command, true, err
	}
	o.pending = nil
	return result.Command, true, nil
}
