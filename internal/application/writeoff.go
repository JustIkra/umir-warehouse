package application

import (
	"context"
	"errors"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func pendingResult(batch domain.Batch, product domain.Product) domain.PendingResult {
	return domain.PendingResult{BatchID: batch.BatchID, ProductID: batch.ProductID, RemainingQuantity: batch.RemainingQuantity, BaseUnit: product.BaseUnit, ExpiresAt: batch.ExpiresAt}
}
func (o *Owner) writePendingLocked(ctx context.Context, batch domain.Batch, now time.Time) (domain.ExpiryResult, error) {
	id := "expiration/pending/" + batch.BatchID
	p := pendingResult(batch, o.state.Products[batch.ProductID])
	event := domain.Event{EventID: o.newID(), TenantID: o.scope.RestaurantID, RuntimeEpoch: o.scope.RuntimeEpoch, SchemaVersion: domain.InventorySchemaV2, RecordedAt: now, Type: domain.EventBatchExpiryPending, Payload: domain.EventPayload{Pending: &p}}
	record, found, err := o.appendOwnerCommandLocked(ctx, domain.CommandBatch{ExpectedVersion: o.state.StateVersion, CommandID: id, CommandType: "PrepareExpiry", CommandFingerprint: domain.Fingerprint("PrepareExpiry", []byte(o.scope.RestaurantID+"/"+o.scope.RuntimeEpoch+"/"+batch.BatchID)), Events: []domain.Event{event}})
	if err != nil {
		return domain.ExpiryResult{}, err
	}
	if !found || record.Result.Pending == nil {
		return domain.ExpiryResult{}, domain.ErrUnknownWriteOutcome
	}
	return domain.ExpiryResult{BatchID: p.BatchID, ProductID: p.ProductID, ExpiresAt: p.ExpiresAt, ProcessedAt: now}, nil
}
func (o *Owner) ConfirmWriteoff(ctx context.Context, request contract.ConfirmWriteoffRequest) (result contract.WriteoffResult, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var committedPreparation int64
	defer func() {
		if err != nil && committedPreparation > 0 {
			err = &contract.WriteoffPreparationError{Cause: err, CommittedVersion: committedPreparation}
		}
	}()
	if request.CommandID == "" || isReservedCommandID(request.CommandID) || request.BatchID == "" || !contract.ValidEntityID(request.ActorUserID) || o.scope.RestaurantID == "" {
		return contract.WriteoffResult{}, contract.ErrInvalidArgument
	}
	captured := o.scope
	if request.Scope.RestaurantID != "" {
		if err := request.Scope.Validate(); err != nil {
			return contract.WriteoffResult{}, err
		}
		if request.Scope.OrganizationID != o.scope.OrganizationID || request.Scope.RestaurantID != o.scope.RestaurantID || request.Scope.RuntimeEpoch != o.scope.RuntimeEpoch || request.Scope.LifecycleVersion != o.scope.LifecycleVersion {
			return contract.WriteoffResult{}, contract.ErrForbidden
		}
		captured = request.Scope
	}
	fingerprint, err := contract.WriteoffFingerprint(o.scope, request.BatchID)
	if err != nil {
		return contract.WriteoffResult{}, err
	}
	for {
		record, found, err := o.findCommand(ctx, request.CommandID, "ConfirmWriteoff", fingerprint)
		if err != nil {
			return contract.WriteoffResult{}, err
		}
		if found {
			return recordedWriteoff(record, fingerprint)
		}
		prepared, transitions, err := o.prepareLocked(ctx)
		if len(transitions) > 0 {
			committedPreparation = o.state.StateVersion
		}
		if err != nil {
			return contract.WriteoffResult{}, inventoryUnavailable(err)
		}
		batch, ok := prepared.State.Batches[request.BatchID]
		if !ok {
			return contract.WriteoffResult{}, contract.ErrBatchNotFound
		}
		if batch.WrittenOffForExpiry > 0 {
			return contract.WriteoffResult{}, contract.ErrAlreadyWrittenOff
		}
		if !batch.PendingWriteoff || batch.RemainingQuantity <= 0 {
			return contract.WriteoffResult{}, contract.ErrNotPendingWriteoff
		}
		eventID := o.newID()
		if eventID == "" {
			return contract.WriteoffResult{}, contract.ErrInvalidArgument
		}
		w := domain.WriteoffResult{BatchID: batch.BatchID, ProductID: batch.ProductID, WrittenOffQuantity: batch.RemainingQuantity, BaseUnit: prepared.State.Products[batch.ProductID].BaseUnit, ActorUserID: request.ActorUserID, ConfirmedAt: prepared.EvaluatedAt, EventID: eventID, OrganizationID: o.scope.OrganizationID, RestaurantID: o.scope.RestaurantID, RuntimeEpoch: o.scope.RuntimeEpoch, TimeZone: captured.TimeZone, SettingsVersion: captured.SettingsVersion, StateVersion: prepared.StateVersion + 1}
		event := domain.Event{EventID: eventID, TenantID: o.scope.RestaurantID, RuntimeEpoch: o.scope.RuntimeEpoch, SchemaVersion: domain.InventorySchemaV2, RecordedAt: prepared.EvaluatedAt, Type: domain.EventBatchWriteoffConfirmed, Payload: domain.EventPayload{Writeoff: &w}}
		command := domain.CommandBatch{ExpectedVersion: prepared.StateVersion, CommandID: request.CommandID, CommandType: "ConfirmWriteoff", CommandFingerprint: fingerprint, Events: []domain.Event{event}}
		if _, err := domain.ApplyRecordedBatch(prepared.State, asRecordedBatch(command)); err != nil {
			return contract.WriteoffResult{}, err
		}
		record, found, err = o.appendOwnerCommandLocked(ctx, command)
		if errors.Is(err, domain.ErrConcurrentModification) {
			continue
		}
		if err != nil {
			return contract.WriteoffResult{}, err
		}
		if !found {
			return contract.WriteoffResult{}, domain.ErrUnknownWriteOutcome
		}
		return recordedWriteoff(record, fingerprint)
	}
}
func (o *Owner) ResolveWriteoff(ctx context.Context, commandID, batchID string) (contract.WriteoffObservation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if commandID == "" || isReservedCommandID(commandID) || batchID == "" || o.scope.RestaurantID == "" {
		return contract.WriteoffObservation{}, contract.ErrInvalidArgument
	}
	fingerprint, err := contract.WriteoffFingerprint(o.scope, batchID)
	if err != nil {
		return contract.WriteoffObservation{}, err
	}
	record, found, err := o.events.FindCommand(ctx, commandID)
	if err != nil {
		return contract.WriteoffObservation{}, inventoryUnavailable(err)
	}
	version, err := o.events.Version(ctx)
	if err != nil {
		return contract.WriteoffObservation{}, inventoryUnavailable(err)
	}
	result := contract.WriteoffObservation{State: "unseen", Fingerprint: fingerprint, EvaluatedAt: domain.CanonicalBusinessTime(o.now()), StateVersion: version}
	if found {
		w, err := recordedWriteoff(record, fingerprint)
		if err != nil {
			return contract.WriteoffObservation{}, err
		}
		result.State = "committed"
		result.Result = &w
	} else if o.pending != nil && o.pending.CommandID == commandID {
		if o.pending.CommandType != "ConfirmWriteoff" || o.pending.CommandFingerprint != fingerprint {
			return contract.WriteoffObservation{}, contract.ErrCommandIdentityConflict
		}
		result.State = "pending"
	}
	return result, nil
}
func recordedWriteoff(record domain.CommandRecord, fingerprint string) (contract.WriteoffResult, error) {
	if record.CommandType != "ConfirmWriteoff" || record.CommandFingerprint != fingerprint {
		return contract.WriteoffResult{}, contract.ErrCommandIdentityConflict
	}
	if record.Result.Kind != domain.CommandResultBatchWriteoffConfirmed || record.Result.Writeoff == nil {
		return contract.WriteoffResult{}, domain.ErrInvalidRecordedBatch
	}
	return toContractWriteoff(*record.Result.Writeoff), nil
}
func toContractWriteoff(v domain.WriteoffResult) contract.WriteoffResult {
	return contract.WriteoffResult{BatchID: v.BatchID, ProductID: v.ProductID, WrittenOffQuantity: contract.Quantity(v.WrittenOffQuantity), BaseUnit: contract.BaseUnit(v.BaseUnit), ActorUserID: v.ActorUserID, ConfirmedAt: v.ConfirmedAt, EventID: v.EventID, OrganizationID: v.OrganizationID, RestaurantID: v.RestaurantID, RuntimeEpoch: v.RuntimeEpoch, TimeZone: v.TimeZone, SettingsVersion: v.SettingsVersion, StateVersion: v.StateVersion}
}
