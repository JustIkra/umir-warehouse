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
	o.mu.Lock()
	defer o.mu.Unlock()
	if request.CommandID == "" || isReservedCommandID(request.CommandID) {
		return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
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
		if request.DeliveryID == "" {
			return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
		}
		if err := o.syncProjectionLocked(ctx); err != nil {
			return contract.DeliveryReceipt{}, inventoryUnavailable(err)
		}
		if _, exists := o.state.Deliveries[request.DeliveryID]; exists {
			return contract.DeliveryReceipt{}, contract.ErrDeliveryAlreadyReceived
		}
		if request.Number == "" || len(request.Lines) == 0 || !businessTime(request.CreatedAt) {
			return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
		}
		now := domain.CanonicalBusinessTime(o.now())
		if request.CreatedAt.After(now) {
			return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
		}
		delivery, batches, err := validateDelivery(o.state, request, now)
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
			if _, exists := o.state.Batches[batchID]; exists {
				return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
			}
			batchIDs[batchID] = struct{}{}
			batches[index].BatchID = batchID
		}
		eventID := o.newID()
		if eventID == "" {
			return contract.DeliveryReceipt{}, contract.ErrInvalidArgument
		}
		seenEventIDs := map[string]struct{}{eventID: {}}
		deliveryEvent := domain.Event{EventID: eventID, RecordedAt: now, Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{Delivery: &delivery, Batches: batches}}
		temporary, err := deliveryProjection(o.state, request.CommandID, fingerprint, deliveryEvent)
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
			events = append(events, domain.Event{EventID: expiryEventID, RecordedAt: now, Type: domain.EventBatchExpired, Payload: domain.EventPayload{Expiry: &expiry}})
		}
		command := domain.CommandBatch{ExpectedVersion: o.state.StateVersion, CommandID: request.CommandID, CommandType: "ReceiveDelivery", CommandFingerprint: fingerprint, Events: events}
		if _, err := domain.ApplyRecordedBatch(o.state, asRecordedBatch(command)); err != nil {
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

func validateDelivery(state domain.InventoryState, request contract.ReceiveDeliveryRequest, now time.Time) (domain.Delivery, []domain.Batch, error) {
	delivery := domain.Delivery{DeliveryID: request.DeliveryID, Number: request.Number, CreatedAt: domain.CanonicalBusinessTime(request.CreatedAt), Lines: make([]domain.DeliveryLine, len(request.Lines))}
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
		if err != nil {
			return domain.Delivery{}, nil, contract.ErrInvalidArgument
		}
		line := domain.DeliveryLine{ProductID: input.ProductID, ManufacturedAt: manufacturedAt, ShelfLife: life, ReceivedAt: receivedAt, Quantity: domain.Quantity(input.Quantity)}
		delivery.Lines[index] = line
		batches[index] = domain.Batch{DeliveryID: request.DeliveryID, LineNo: int64(index + 1), ProductID: product.ProductID, ManufacturedAt: manufacturedAt, ShelfLife: life, ReceivedAt: receivedAt, ExpiresAt: expiresAt, ReceivedQuantity: line.Quantity, RemainingQuantity: line.Quantity}
	}
	return delivery, batches, nil
}

func deliveryProjection(state domain.InventoryState, commandID, fingerprint string, event domain.Event) (domain.InventoryState, error) {
	command := domain.CommandBatch{ExpectedVersion: state.StateVersion, CommandID: commandID, CommandType: "ReceiveDelivery", CommandFingerprint: fingerprint, Events: []domain.Event{event}}
	projected, err := domain.ApplyRecordedBatch(state, asRecordedBatch(command))
	if err != nil {
		return domain.InventoryState{}, err
	}
	return projected, nil
}

func asRecordedBatch(command domain.CommandBatch) domain.RecordedBatch {
	events := make([]domain.Event, len(command.Events))
	for index, source := range command.Events {
		event := domain.CloneEvent(source)
		event.StreamID = domain.InventoryStreamID
		event.Sequence = command.ExpectedVersion + int64(index) + 1
		event.SchemaVersion = domain.InventorySchemaV1
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
	return contract.DeliveryReceipt{DeliveryID: record.Result.Delivery.DeliveryID, BatchIDs: append([]string(nil), record.Result.Delivery.BatchIDs...)}, nil
}

func businessTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 3*60*60
}

func fingerprintDelivery(request contract.ReceiveDeliveryRequest) string {
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
