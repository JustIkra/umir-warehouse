package application

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func (o *Owner) ReceiveDelivery(ctx context.Context, request contract.ReceiveDeliveryRequest) (contract.DeliveryReceipt, error) {
	if err := o.mu.Lock(ctx); err != nil {
		return contract.DeliveryReceipt{}, inventoryUnavailable(err)
	}
	defer o.mu.Unlock()
	if request.CommandID == "" || isReservedCommandID(request.CommandID) {
		return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
	}
	if request.Scope.RestaurantID == "" {
		request.Scope = o.scope
	}
	if request.Scope.RestaurantID != "" && (request.Scope.Validate() != nil || request.Scope.OrganizationID != o.scope.OrganizationID || request.Scope.RestaurantID != o.scope.RestaurantID || request.Scope.RuntimeEpoch != o.scope.RuntimeEpoch || request.Scope.LifecycleVersion != o.scope.LifecycleVersion) {
		return contract.DeliveryReceipt{}, contract.ErrForbidden
	}
	fingerprint := fingerprintDelivery(request)
	for {
		record, found, err := o.findCommand(ctx, request.CommandID, "ReceiveDelivery", fingerprint)
		if err != nil {
			return contract.DeliveryReceipt{}, err
		}
		if found {
			return recordedDelivery(record, fingerprint)
		}
		if request.TimeInputs != nil {
			if request.Scope.Validate() != nil || request.Scope.OrganizationID != o.scope.OrganizationID || request.Scope.RestaurantID != o.scope.RestaurantID || request.Scope.RuntimeEpoch != o.scope.RuntimeEpoch || request.Scope.LifecycleVersion != o.scope.LifecycleVersion {
				return contract.DeliveryReceipt{}, contract.ErrForbidden
			}
			if request.ExpectedSettingsVersion != request.Scope.SettingsVersion {
				return contract.DeliveryReceipt{}, contract.ErrSettingsChanged
			}
			if o.tzdataVersion == "" {
				return contract.DeliveryReceipt{}, contract.ErrTimeZoneDataUnavailable
			}
			resolved, err := resolveDeliveryTimes(ctx, request, o.loadZone)
			if err != nil {
				return contract.DeliveryReceipt{}, err
			}
			request = resolved
		}
		if request.DeliveryID == "" {
			return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
		}
		if err := o.syncProjectionLocked(ctx); err != nil {
			return contract.DeliveryReceipt{}, inventoryUnavailable(err)
		}
		if _, exists := o.state.DeliveryIDs[request.DeliveryID]; exists {
			return contract.DeliveryReceipt{}, contract.ErrDeliveryAlreadyReceived
		}
		if request.Number == "" || len(request.Lines) == 0 || !businessTime(request.CreatedAt) {
			return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
		}
		validationTime := domain.CanonicalBusinessTime(o.now())
		if request.CreatedAt.After(validationTime) {
			return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
		}
		if _, _, err := validateDelivery(o.state, request, validationTime, o.loadZone); err != nil {
			return contract.DeliveryReceipt{}, err
		}
		prepared, _, err := o.prepareLocked(ctx)
		if err != nil {
			return contract.DeliveryReceipt{}, inventoryUnavailable(err)
		}
		record, found, err = o.findCommand(ctx, request.CommandID, "ReceiveDelivery", fingerprint)
		if err != nil {
			return contract.DeliveryReceipt{}, err
		}
		if found {
			return recordedDelivery(record, fingerprint)
		}
		if _, exists := o.state.DeliveryIDs[request.DeliveryID]; exists {
			return contract.DeliveryReceipt{}, contract.ErrDeliveryAlreadyReceived
		}
		now := prepared.EvaluatedAt
		if request.CreatedAt.After(now) {
			return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
		}
		delivery, batches, err := validateDelivery(prepared.State, request, now, o.loadZone)
		if err != nil {
			return contract.DeliveryReceipt{}, err
		}
		batchIDs := make(map[string]struct{}, len(batches))
		for index := range batches {
			batchID := o.newID()
			if batchID == "" {
				return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
			}
			if _, exists := batchIDs[batchID]; exists {
				return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
			}
			if _, exists := prepared.State.Batches[batchID]; exists {
				return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
			}
			batchIDs[batchID] = struct{}{}
			batches[index].BatchID = batchID
			if o.scope.RestaurantID != "" {
				batches[index].AppliedTimeZone = request.Scope.TimeZone
				batches[index].AppliedSettingsVersion = request.Scope.SettingsVersion
				batches[index].TZDataVersion = o.tzdataVersion
				if batches[index].TZDataVersion == "" {
					batches[index].TZDataVersion = "unknown"
				}
			}
		}
		eventID := o.newID()
		if eventID == "" {
			return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
		}
		seenEventIDs := map[string]struct{}{eventID: {}}
		deliveryEvent := domain.Event{EventID: eventID, RecordedAt: now, Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{Delivery: &delivery, Batches: batches}}
		if o.scope.RestaurantID != "" {
			deliveryEvent.TenantID = o.scope.RestaurantID
			deliveryEvent.RuntimeEpoch = o.scope.RuntimeEpoch
			deliveryEvent.SchemaVersion = domain.InventorySchemaV2
		}
		deliveryOnly := domain.CommandBatch{ExpectedVersion: prepared.StateVersion, CommandID: request.CommandID, CommandType: "ReceiveDelivery", CommandFingerprint: fingerprint, Events: []domain.Event{deliveryEvent}}
		temporary, err := domain.PlanBatch(&o.state, asRecordedBatch(deliveryOnly))
		if err != nil {
			return contract.DeliveryReceipt{}, err
		}
		due := make([]domain.Batch, 0, len(batches))
		for _, batch := range batches {
			if !batch.ExpiresAt.After(now) {
				due = append(due, temporary.Batches[batch.BatchID])
			}
		}
		sort.Slice(due, func(i, j int) bool {
			if due[i].ExpiresAt.Equal(due[j].ExpiresAt) {
				return due[i].BatchID < due[j].BatchID
			}
			return due[i].ExpiresAt.Before(due[j].ExpiresAt)
		})
		events := []domain.Event{deliveryEvent}
		for _, batch := range due {
			expiryEventID := o.newID()
			if expiryEventID == "" {
				return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
			}
			if _, exists := seenEventIDs[expiryEventID]; exists {
				return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
			}
			seenEventIDs[expiryEventID] = struct{}{}
			expiry := domain.ExpiryResult{BatchID: batch.BatchID, ProductID: batch.ProductID, WrittenOffQuantity: batch.RemainingQuantity, Reason: domain.ExpiryReasonExpiration, ExpiresAt: batch.ExpiresAt, ProcessedAt: now}
			if o.scope.RestaurantID != "" {
				p := pendingResult(batch, prepared.State.Products[batch.ProductID])
				events = append(events, domain.Event{EventID: expiryEventID, TenantID: o.scope.RestaurantID, RuntimeEpoch: o.scope.RuntimeEpoch, SchemaVersion: domain.InventorySchemaV2, RecordedAt: now, Type: domain.EventBatchExpiryPending, Payload: domain.EventPayload{Pending: &p}})
			} else {
				events = append(events, domain.Event{EventID: expiryEventID, RecordedAt: now, Type: domain.EventBatchExpired, Payload: domain.EventPayload{Expiry: &expiry}})
			}
		}
		command := domain.CommandBatch{ExpectedVersion: prepared.StateVersion, CommandID: request.CommandID, CommandType: "ReceiveDelivery", CommandFingerprint: fingerprint, Events: events}
		planned, err := domain.PlanBatch(&o.state, asRecordedBatch(command))
		if err != nil {
			return contract.DeliveryReceipt{}, err
		}
		if err := domain.ValidateInventoryTotalsWith(&o.state, planned); err != nil {
			return contract.DeliveryReceipt{}, err
		}
		record, found, err = o.appendOwnerCommandLocked(ctx, command)
		if errors.Is(err, domain.ErrConcurrentModification) {
			continue
		}
		if err != nil {
			return contract.DeliveryReceipt{}, err
		}
		if !found {
			return contract.DeliveryReceipt{}, domain.ErrUnknownWriteOutcome
		}
		return recordedDelivery(record, fingerprint)
	}
}

func validateDelivery(state domain.InventoryState, request contract.ReceiveDeliveryRequest, now time.Time, loadZone func(string) (*time.Location, error)) (domain.Delivery, []domain.Batch, error) {
	delivery := domain.Delivery{DeliveryID: request.DeliveryID, Number: request.Number, CreatedAt: domain.CanonicalBusinessTime(request.CreatedAt), Lines: make([]domain.DeliveryLine, len(request.Lines))}
	if request.Scope.RestaurantID != "" {
		delivery.OrganizationID = request.Scope.OrganizationID
		delivery.LifecycleVersion = request.Scope.LifecycleVersion
	}
	batches := make([]domain.Batch, len(request.Lines))
	for index, input := range request.Lines {
		if input.ProductID == "" || input.Quantity <= 0 || input.ShelfLife.Value <= 0 || input.ShelfLife.Unit != contract.ShelfLifeDay && input.ShelfLife.Unit != contract.ShelfLifeMonth {
			return domain.Delivery{}, nil, contract.ErrInvalidArgument
		}
		product, exists := state.Products[input.ProductID]
		if !exists {
			return domain.Delivery{}, nil, contract.ErrProductNotFound
		}
		if !businessTime(input.ManufacturedAt) || !businessTime(input.ReceivedAt) {
			return domain.Delivery{}, nil, contract.ErrInvalidArgument
		}
		manufacturedAt := domain.CanonicalBusinessTime(input.ManufacturedAt)
		receivedAt := domain.CanonicalBusinessTime(input.ReceivedAt)
		if manufacturedAt.After(receivedAt) || receivedAt.After(now) {
			return domain.Delivery{}, nil, contract.ErrInvalidArgument
		}
		life := domain.ShelfLife{Value: input.ShelfLife.Value, Unit: domain.ShelfLifeUnit(input.ShelfLife.Unit)}
		expiresAt, err := domain.CalculateExpiresAt(manufacturedAt, life)
		if request.Scope.RestaurantID != "" {
			zone, zoneErr := loadZone(request.Scope.TimeZone)
			if zoneErr != nil {
				return domain.Delivery{}, nil, calendarError(zoneErr)
			}
			expiresAt, err = domain.CalculateExpiresAtInZone(manufacturedAt, life, zone)
		}
		if err != nil {
			return domain.Delivery{}, nil, contract.ErrInvalidArgument
		}
		line := domain.DeliveryLine{ProductID: input.ProductID, ManufacturedAt: manufacturedAt, ShelfLife: life, ReceivedAt: receivedAt, Quantity: domain.Quantity(input.Quantity)}
		delivery.Lines[index] = line
		batches[index] = domain.Batch{DeliveryID: request.DeliveryID, LineNo: int64(index + 1), ProductID: product.ProductID, ManufacturedAt: manufacturedAt, ShelfLife: life, ReceivedAt: receivedAt, ExpiresAt: expiresAt, ReceivedQuantity: line.Quantity, RemainingQuantity: line.Quantity}
	}
	return delivery, batches, nil
}

func asRecordedBatch(command domain.CommandBatch) domain.RecordedBatch {
	events := make([]domain.Event, len(command.Events))
	for index, source := range command.Events {
		event := domain.CloneEvent(source)
		event.StreamID = domain.InventoryStreamID
		event.Sequence = command.ExpectedVersion + int64(index) + 1
		event.SchemaVersion = domain.EventSchemaVersion(event)
		event.CommandID = command.CommandID
		event.CommandFingerprint = command.CommandFingerprint
		events[index] = event
	}
	return domain.RecordedBatch{CommandID: command.CommandID, CommandType: command.CommandType, CommandFingerprint: command.CommandFingerprint, Events: events, FinalSequence: command.ExpectedVersion + int64(len(events))}
}

func recordedDelivery(record domain.CommandRecord, fingerprint string) (contract.DeliveryReceipt, error) {
	if record.CommandType != "ReceiveDelivery" || record.CommandFingerprint != fingerprint {
		return contract.DeliveryReceipt{}, contract.ErrCommandIdentityConflict
	}
	if record.Result.Kind != domain.CommandResultDeliveryReceived || record.Result.Delivery == nil || record.Result.Delivery.DeliveryID == "" || len(record.Result.Delivery.BatchIDs) == 0 {
		return contract.DeliveryReceipt{}, domain.ErrInvalidRecordedBatch
	}
	value := record.Result.Delivery
	result := contract.DeliveryReceipt{DeliveryID: value.DeliveryID, BatchIDs: append([]string(nil), value.BatchIDs...), EvaluatedAt: value.EvaluatedAt, StateVersion: value.StateVersion}
	for _, b := range value.Batches {
		result.Batches = append(result.Batches, toContractBatch(b))
	}
	if len(result.Batches) > 0 {
		b := result.Batches[0]
		result.Scope = contract.Scope{OrganizationID: value.OrganizationID, RestaurantID: value.RestaurantID, RuntimeEpoch: value.RuntimeEpoch, LifecycleVersion: value.LifecycleVersion, TimeZone: b.AppliedTimeZone, SettingsVersion: b.AppliedSettingsVersion}
	}
	return result, nil
}

func businessTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 3*60*60
}

func fingerprintDelivery(request contract.ReceiveDeliveryRequest) string {
	if request.TimeInputs != nil {
		type line struct {
			ProductID string
			ShelfLife contract.ShelfLife
			Quantity  contract.Quantity
		}
		lines := make([]line, len(request.Lines))
		for i, v := range request.Lines {
			lines[i] = line{v.ProductID, v.ShelfLife, v.Quantity}
		}
		return fingerprintJSON("ReceiveDeliveryV2", struct {
			DeliveryID, Number, ExpectedSettingsVersion string
			Times                                       contract.DeliveryTimeInputs
			Lines                                       []line
		}{request.DeliveryID, request.Number, request.ExpectedSettingsVersion, *request.TimeInputs, lines})
	}
	type lineInput struct {
		ProductID      string
		ManufacturedAt string
		ShelfLife      contract.ShelfLife
		ReceivedAt     string
		Quantity       contract.Quantity
	}
	lines := make([]lineInput, len(request.Lines))
	for index, line := range request.Lines {
		lines[index] = lineInput{ProductID: line.ProductID, ManufacturedAt: canonicalCommandTime(line.ManufacturedAt), ShelfLife: line.ShelfLife, ReceivedAt: canonicalCommandTime(line.ReceivedAt), Quantity: line.Quantity}
	}
	return fingerprintJSON("ReceiveDelivery", struct {
		DeliveryID string
		Number     string
		CreatedAt  string
		Lines      []lineInput
	}{DeliveryID: request.DeliveryID, Number: request.Number, CreatedAt: canonicalCommandTime(request.CreatedAt), Lines: lines})
}
