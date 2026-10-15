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
		o.usable = false
		return err
	}
	if version < o.state.StateVersion {
		o.usable = false
		return o.recoverCurrentLocked(ctx)
	}
	if version == o.state.StateVersion {
		return nil
	}
	batches, err := o.events.ReadBatches(ctx, o.state.StateVersion)
	if err != nil {
		o.usable = false
		return err
	}
	for _, batch := range batches {
		if err := o.applyBatchLocked(batch); err != nil {
			o.usable = false
			return o.recoverCurrentLocked(ctx)
		}
	}
	if o.state.StateVersion != version {
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

func (o *Owner) recoverLocked(ctx context.Context) error {
	o.started = true
	o.usable = false
	batches, err := o.events.ReadBatches(ctx, 0)
	if err != nil {
		return err
	}
	version, err := o.events.Version(ctx)
	if err != nil {
		return err
	}
	prefixVersion := int64(0)
	if len(batches) > 0 {
		prefixVersion = batches[len(batches)-1].FinalSequence
	}
	if err := validateJournalContext(ctx, batches, prefixVersion); err != nil {
		return err
	}
	if version < prefixVersion {
		return domain.ErrInvalidRecordedBatch
	}
	if version > prefixVersion {
		return domain.ErrConcurrentModification
	}
	var candidate domain.Snapshot
	hasCandidate := false
	if o.snapshotStore != nil {
		candidate, hasCandidate, err = o.snapshotStore.LoadLatest(ctx)
		if err != nil {
			return err
		}
	}
	state, usedSnapshot := domain.InventoryState{}, false
	if hasCandidate && candidate.LastSequence <= version && domain.ValidateSnapshot(candidate) == nil {
		fromSnapshot := domain.CloneInventoryState(candidate.State)
		if replayAfter(&fromSnapshot, batches, candidate.LastSequence) == nil && fromSnapshot.StateVersion == version {
			state, usedSnapshot = fromSnapshot, true
			o.lastSavedSequence = max(o.lastSavedSequence, candidate.LastSequence)
			o.snapshotWorker.observeSaved(candidate.LastSequence)
		}
	}
	if !usedSnapshot {
		if err := replayAfter(&state, batches, 0); err != nil {
			return err
		}
		if state.StateVersion != version {
			return domain.ErrInvalidRecordedBatch
		}
	}
	index, err := expirationIndex(state)
	if err != nil {
		return err
	}
	o.state, o.expirations = state, index
	o.publishProjection()
	if o.pending != nil {
		record, found, err := o.events.FindCommand(ctx, o.pending.CommandID)
		if err != nil {
			o.usable = false
			return err
		}
		if found {
			if record.CommandType != o.pending.CommandType || record.CommandFingerprint != o.pending.CommandFingerprint {
				o.usable = false
				return domain.ErrCommandIdentityConflict
			}
			o.pending = nil
		}
	}
	return nil
}

func replayAfter(state *domain.InventoryState, batches []domain.RecordedBatch, after int64) error {
	for _, batch := range batches {
		if batch.FinalSequence <= after {
			continue
		}
		if err := state.ApplyBatch(batch); err != nil {
			return err
		}
	}
	return nil
}

func (o *Owner) publishProjection() {
	o.usable = true
	o.offerSnapshotLocked()
}

func validateJournalContext(ctx context.Context, batches []domain.RecordedBatch, version int64) error {
	if version < 0 {
		return domain.ErrInvalidRecordedBatch
	}
	expected := int64(1)
	commands := make(map[string]struct{}, len(batches))
	eventIDs := make(map[string]struct{})
	for _, batch := range batches {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(batch.Events) == 0 || batch.CommandID == "" || batch.CommandType == "" || batch.CommandFingerprint == "" || batch.FinalSequence != batch.Events[len(batch.Events)-1].Sequence {
			return domain.ErrInvalidRecordedBatch
		}
		if _, exists := commands[batch.CommandID]; exists {
			return domain.ErrInvalidRecordedBatch
		}
		commands[batch.CommandID] = struct{}{}
		if int64(len(batch.Events)) > math.MaxInt64-expected {
			return domain.ErrInvalidRecordedBatch
		}
		for _, event := range batch.Events {
			if err := ctx.Err(); err != nil {
				return err
			}
			if event.EventID == "" {
				return domain.ErrInvalidRecordedBatch
			}
			if _, exists := eventIDs[event.EventID]; exists {
				return domain.ErrInvalidRecordedBatch
			}
			eventIDs[event.EventID] = struct{}{}
			if err := domain.ValidateRecordedEvent(event, batch, expected); err != nil {
				return err
			}
			expected++
		}
		if _, err := domain.ResultForBatch(batch); err != nil {
			return err
		}
	}
	if expected-1 != version {
		return domain.ErrInvalidRecordedBatch
	}
	return nil
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

// applyBatchLocked applies one recorded batch to the state and the expiration
// index in place. The batch is planned first, so a rejected batch changes nothing.
func (o *Owner) applyBatchLocked(batch domain.RecordedBatch) error {
	delta, err := domain.PlanBatch(&o.state, batch)
	if err != nil {
		return err
	}
	o.state.Merge(delta)
	if o.expirations == nil {
		o.expirations = make(map[string]time.Time, len(delta.Batches))
	}
	for batchID, b := range delta.Batches {
		if b.RemainingQuantity > 0 && !b.PendingWriteoff {
			o.expirations[batchID] = b.ExpiresAt
		} else {
			delete(o.expirations, batchID)
		}
	}
	return nil
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
		o.usable = false
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
			o.usable = false
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
		o.usable = false
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
