package application

import (
	"context"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func (o *Owner) syncProjectionLocked(ctx context.Context) error {
	if o.pending != nil {
		if err := o.resolvePendingLocked(ctx); err != nil {
			return err
		}
	}
	if !o.usable {
		return o.recoverLocked(ctx)
	}
	version, err := o.events.Version(ctx)
	if err != nil {
		o.usable = false
		return err
	}
	if version < o.state.StateVersion {
		o.usable = false
		return o.recoverLocked(ctx)
	}
	if version == o.state.StateVersion {
		return nil
	}
	batches, err := o.events.ReadBatches(ctx, o.state.StateVersion)
	if err != nil {
		o.usable = false
		return err
	}
	state := domain.CloneInventoryState(o.state)
	index := cloneExpirationIndex(o.expirations)
	for _, batch := range batches {
		state, index, err = applyProjectionBatch(state, index, batch)
		if err != nil {
			o.usable = false
			return o.recoverLocked(ctx)
		}
	}
	if state.StateVersion != version {
		o.usable = false
		return o.recoverLocked(ctx)
	}
	o.publishProjection(state, index)
	return nil
}

func (o *Owner) recoverLocked(ctx context.Context) error {
	o.usable = false
	batches, err := o.events.ReadBatches(ctx, 0)
	if err != nil {
		return err
	}
	state := domain.InventoryState{}
	for _, batch := range batches {
		state, err = domain.ApplyRecordedBatch(state, batch)
		if err != nil {
			return err
		}
	}
	version, err := o.events.Version(ctx)
	if err != nil {
		return err
	}
	if state.StateVersion != version {
		return domain.ErrInvalidRecordedBatch
	}
	index, err := expirationIndex(state)
	if err != nil {
		return err
	}
	o.publishProjection(state, index)
	if o.pending != nil {
		if record, found := state.Commands[o.pending.CommandID]; found {
			if record.CommandType != o.pending.CommandType || record.CommandFingerprint != o.pending.CommandFingerprint {
				o.usable = false
				return domain.ErrCommandIdentityConflict
			}
			o.pending = nil
		}
	}
	return nil
}

func (o *Owner) publishProjection(state domain.InventoryState, index map[string]time.Time) {
	o.state = state
	o.expirations = index
	o.usable = true
}

func expirationIndex(state domain.InventoryState) (map[string]time.Time, error) {
	index := make(map[string]time.Time)
	for batchID, batch := range state.Batches {
		if batchID == "" || batch.BatchID != batchID || batch.RemainingQuantity < 0 {
			return nil, domain.ErrInvalidRecordedBatch
		}
		if batch.RemainingQuantity > 0 {
			index[batchID] = batch.ExpiresAt
		}
	}
	return index, nil
}

func applyProjectionBatch(state domain.InventoryState, index map[string]time.Time, batch domain.RecordedBatch) (domain.InventoryState, map[string]time.Time, error) {
	next, err := domain.ApplyRecordedBatch(state, batch)
	if err != nil {
		return domain.InventoryState{}, nil, err
	}
	updated := cloneExpirationIndex(index)
	for _, event := range batch.Events {
		switch event.Type {
		case domain.EventDeliveryReceived:
			for _, registered := range event.Payload.Batches {
				if batch, exists := next.Batches[registered.BatchID]; exists && batch.RemainingQuantity > 0 {
					updated[registered.BatchID] = batch.ExpiresAt
				} else {
					delete(updated, registered.BatchID)
				}
			}
		case domain.EventOrderPlaced:
			for _, allocation := range event.Payload.Order.Allocations {
				if batch, exists := next.Batches[allocation.BatchID]; exists && batch.RemainingQuantity > 0 {
					updated[allocation.BatchID] = batch.ExpiresAt
				} else {
					delete(updated, allocation.BatchID)
				}
			}
		case domain.EventBatchExpired:
			delete(updated, event.Payload.Expiry.BatchID)
		}
	}
	return next, updated, nil
}

func cloneExpirationIndex(index map[string]time.Time) map[string]time.Time {
	cloned := make(map[string]time.Time, len(index))
	for batchID, dueAt := range index {
		cloned[batchID] = dueAt
	}
	return cloned
}

func (o *Owner) applyCommittedBatch(batch domain.RecordedBatch) error {
	state, index, err := applyProjectionBatch(o.state, o.expirations, batch)
	if err != nil {
		return err
	}
	o.publishProjection(state, index)
	return nil
}

func (o *Owner) resolvePendingLocked(ctx context.Context) error {
	if o.pending == nil {
		return nil
	}
	pending := cloneCommandBatch(*o.pending)
	record, found, err := o.events.FindCommand(ctx, pending.CommandID)
	if err != nil {
		o.usable = false
		return err
	}
	if found {
		if record.CommandType != pending.CommandType || record.CommandFingerprint != pending.CommandFingerprint {
			o.usable = false
			return domain.ErrCommandIdentityConflict
		}
		batches, err := o.events.ReadBatches(ctx, pending.ExpectedVersion)
		if err != nil {
			o.usable = false
			return err
		}
		for _, batch := range batches {
			if batch.CommandID == pending.CommandID {
				if err := o.applyCommittedBatch(batch); err != nil {
					o.usable = false
					return err
				}
				o.pending = nil
				return nil
			}
		}
		o.usable = false
		return domain.ErrInvalidRecordedBatch
	}
	result, err := o.events.AppendBatch(ctx, *pending)
	if err != nil {
		o.usable = false
		return err
	}
	if err := o.applyCommittedBatch(result.Batch); err != nil {
		o.usable = false
		return err
	}
	o.pending = nil
	return nil
}
