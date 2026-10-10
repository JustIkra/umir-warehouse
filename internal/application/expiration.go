package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func (o *Owner) nextDueBatch(now time.Time) (string, bool) {
	var selected string
	var selectedAt time.Time
	for batchID, dueAt := range o.expirations {
		if dueAt.After(now) {
			continue
		}
		if selected == "" || dueAt.Before(selectedAt) || dueAt.Equal(selectedAt) && batchID < selected {
			selected = batchID
			selectedAt = dueAt
		}
	}
	return selected, selected != ""
}

func (o *Owner) ProcessDueExpirations(ctx context.Context) ([]contract.ExpiryResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, results, err := o.prepareLocked(ctx)
	if err != nil {
		return results, inventoryUnavailable(err)
	}
	return results, nil
}

func (o *Owner) ExpireBatch(ctx context.Context, request contract.ExpireBatchRequest) (contract.ExpiryResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if request.CommandID == "" || request.BatchID == "" {
		return contract.ExpiryResult{}, contract.ErrInvalidArgument
	}
	if request.CommandID != "expiration/"+request.BatchID {
		return contract.ExpiryResult{}, contract.ErrInvalidArgument
	}
	fingerprint := domain.Fingerprint("ExpireBatch", []byte(request.BatchID))
	record, found, err := o.events.FindCommand(ctx, request.CommandID)
	if err != nil {
		return contract.ExpiryResult{}, inventoryUnavailable(err)
	}
	if found {
		return recordedExpiry(record, fingerprint, request.BatchID)
	}
	if err := o.syncProjectionLocked(ctx); err != nil {
		return contract.ExpiryResult{}, inventoryUnavailable(err)
	}
	batch, ok := o.state.Batches[request.BatchID]
	if !ok {
		return contract.ExpiryResult{}, domain.ErrBatchNotFound
	}
	if batch.RemainingQuantity <= 0 {
		return contract.ExpiryResult{}, contract.ErrNotDue
	}
	now := domain.CanonicalBusinessTime(o.now())
	if now.Before(batch.ExpiresAt) {
		return contract.ExpiryResult{}, contract.ErrNotDue
	}
	result, err := o.writeExpiryLocked(ctx, request.CommandID, fingerprint, batch, now)
	if err != nil {
		return contract.ExpiryResult{}, inventoryUnavailable(err)
	}
	return toContractExpiry(result), nil
}

func (o *Owner) expireDueLocked(ctx context.Context, batchID string, now time.Time) (domain.ExpiryResult, error) {
	batch, ok := o.state.Batches[batchID]
	if !ok || batch.RemainingQuantity <= 0 {
		o.usable = false
		return domain.ExpiryResult{}, domain.ErrInvalidRecordedBatch
	}
	return o.writeExpiryLocked(ctx, "expiration/"+batchID, domain.Fingerprint("ExpireBatch", []byte(batchID)), batch, now)
}

func (o *Owner) writeExpiryLocked(ctx context.Context, commandID, fingerprint string, batch domain.Batch, now time.Time) (domain.ExpiryResult, error) {
	result := domain.ExpiryResult{BatchID: batch.BatchID, ProductID: batch.ProductID, WrittenOffQuantity: batch.RemainingQuantity, Reason: domain.ExpiryReasonExpiration, ExpiresAt: batch.ExpiresAt, ProcessedAt: now}
	event := domain.Event{EventID: o.newID(), RecordedAt: now, Type: domain.EventBatchExpired, Payload: domain.EventPayload{Expiry: &result}}
	command := domain.CommandBatch{ExpectedVersion: o.state.StateVersion, CommandID: commandID, CommandType: "ExpireBatch", CommandFingerprint: fingerprint, Events: []domain.Event{event}}
	appendResult, err := o.events.AppendBatch(ctx, command)
	if errors.Is(err, domain.ErrUnknownWriteOutcome) {
		o.pending = cloneCommandBatch(command)
		if err := o.resolvePendingLocked(ctx); err != nil {
			return domain.ExpiryResult{}, err
		}
		record, found, err := o.events.FindCommand(ctx, commandID)
		if err != nil || !found {
			if err == nil {
				err = domain.ErrUnknownWriteOutcome
			}
			return domain.ExpiryResult{}, err
		}
		return recordedDomainExpiry(record, fingerprint, batch.BatchID)
	}
	if err != nil {
		return domain.ExpiryResult{}, err
	}
	if appendResult.Command.CommandID != commandID || appendResult.Command.CommandFingerprint != fingerprint {
		o.usable = false
		return domain.ExpiryResult{}, domain.ErrInvalidRecordedBatch
	}
	if err := o.applyCommittedBatch(appendResult.Batch); err != nil {
		o.usable = false
		if appendResult.Command.Result.Expiry != nil {
			return domain.CloneExpiryResult(*appendResult.Command.Result.Expiry), nil
		}
		return domain.ExpiryResult{}, err
	}
	return recordedDomainExpiry(appendResult.Command, fingerprint, batch.BatchID)
}

func recordedExpiry(record domain.CommandRecord, fingerprint, batchID string) (contract.ExpiryResult, error) {
	if record.CommandType != "ExpireBatch" || record.CommandFingerprint != fingerprint {
		return contract.ExpiryResult{}, contract.ErrCommandIdentityConflict
	}
	result, err := recordedDomainExpiry(record, fingerprint, batchID)
	if err != nil {
		return contract.ExpiryResult{}, err
	}
	return toContractExpiry(result), nil
}

func recordedDomainExpiry(record domain.CommandRecord, fingerprint, batchID string) (domain.ExpiryResult, error) {
	if record.CommandType != "ExpireBatch" || record.CommandFingerprint != fingerprint {
		return domain.ExpiryResult{}, contract.ErrCommandIdentityConflict
	}
	if record.Result.Kind != domain.CommandResultBatchExpired || record.Result.Expiry == nil || record.Result.Expiry.BatchID != batchID {
		return domain.ExpiryResult{}, domain.ErrInvalidRecordedBatch
	}
	return domain.CloneExpiryResult(*record.Result.Expiry), nil
}

func toContractExpiry(value domain.ExpiryResult) contract.ExpiryResult {
	return contract.ExpiryResult{BatchID: value.BatchID, ProductID: value.ProductID, WrittenOffQuantity: contract.Quantity(value.WrittenOffQuantity), Reason: value.Reason, ExpiresAt: value.ExpiresAt, ProcessedAt: value.ProcessedAt}
}

func cloneCommandBatch(value domain.CommandBatch) *domain.CommandBatch {
	copyValue := value
	copyValue.Events = make([]domain.Event, len(value.Events))
	for i, event := range value.Events {
		copyValue.Events[i] = domain.CloneEvent(event)
	}
	return &copyValue
}

func inventoryUnavailable(err error) error {
	if errors.Is(err, contract.ErrInventoryUnavailable) {
		return err
	}
	return fmt.Errorf("%w: %w", contract.ErrInventoryUnavailable, err)
}
