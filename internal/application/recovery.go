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
				return o.recoverLocked(ctx)
			}
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
	if err := validateJournal(batches, version); err != nil {
		return err
	}
	var candidate domain.Snapshot
	hasCandidate := false
	if o.snapshotStore != nil {
		candidate, hasCandidate, err = o.snapshotStore.LoadLatest(ctx)
		if err != nil {
			return err
		}
	}
	state := domain.InventoryState{}
	startSequence := int64(0)
	if hasCandidate && candidate.LastSequence <= version && auditSnapshotPrefix(candidate, batches) == nil {
		state = domain.CloneInventoryState(candidate.State)
		startSequence = candidate.LastSequence
		o.lastSavedSequence = max(o.lastSavedSequence, candidate.LastSequence)
		o.snapshotWorker.observeSaved(candidate.LastSequence)
	}
	for _, batch := range batches {
		if batch.FinalSequence <= startSequence {
			continue
		}
		state, err = domain.ApplyRecordedBatch(state, batch)
		if err != nil {
			return err
		}
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
	o.offerSnapshotLocked()
}

func validateJournal(batches []domain.RecordedBatch, version int64) error {
	if version < 0 {
		return domain.ErrInvalidRecordedBatch
	}
	expected := int64(1)
	commands := make(map[string]struct{}, len(batches))
	eventIDs := make(map[string]struct{})
	for _, batch := range batches {
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

func auditSnapshotPrefix(snapshot domain.Snapshot, batches []domain.RecordedBatch) error {
	if err := domain.ValidateSnapshot(snapshot); err != nil {
		return err
	}
	if snapshot.LastSequence < 0 {
		return domain.ErrInvalidSnapshot
	}
	if snapshot.LastSequence == 0 {
		if len(snapshot.State.Commands) != 0 {
			return domain.ErrInvalidSnapshot
		}
		return nil
	}
	prefixCount := 0
	boundary := false
	for _, batch := range batches {
		if batch.FinalSequence > snapshot.LastSequence {
			break
		}
		prefixCount++
		boundary = batch.FinalSequence == snapshot.LastSequence
	}
	if !boundary || len(snapshot.State.Commands) != prefixCount {
		return domain.ErrInvalidSnapshot
	}
	products := make(map[string]struct{}, len(snapshot.State.Products))
	dishes := make(map[string]struct{}, len(snapshot.State.Dishes))
	deliveries := make(map[string]struct{}, len(snapshot.State.Deliveries))
	createdBatches := make(map[string]struct{}, len(snapshot.State.Batches))
	expired := make(map[string]domain.Quantity, len(snapshot.State.Batches))
	for _, batch := range batches {
		if batch.FinalSequence > snapshot.LastSequence {
			break
		}
		derived, err := domain.ResultForBatch(batch)
		if err != nil {
			return err
		}
		record, exists := snapshot.State.Commands[batch.CommandID]
		if !exists || record.CommandID != batch.CommandID || record.CommandType != batch.CommandType || record.CommandFingerprint != batch.CommandFingerprint || !sameCommandResult(record.Result, derived) {
			return domain.ErrInvalidSnapshot
		}
		switch batch.CommandType {
		case "RegisterProduct":
			product := batch.Events[0].Payload.Product
			if product == nil || !sameProduct(snapshot.State.Products[product.ProductID], *product) {
				return domain.ErrInvalidSnapshot
			}
			products[product.ProductID] = struct{}{}
		case "DefineDish":
			dish := batch.Events[0].Payload.Dish
			if dish == nil || !sameDish(snapshot.State.Dishes[dish.DishID], *dish) {
				return domain.ErrInvalidSnapshot
			}
			for _, ingredient := range dish.Ingredients {
				if _, found := products[ingredient.ProductID]; !found {
					return domain.ErrInvalidRecordedBatch
				}
			}
			dishes[dish.DishID] = struct{}{}
		case "ReceiveDelivery":
			delivery := batch.Events[0].Payload.Delivery
			if delivery == nil || !sameDelivery(snapshot.State.Deliveries[delivery.DeliveryID], *delivery) {
				return domain.ErrInvalidSnapshot
			}
			for _, line := range delivery.Lines {
				if _, found := products[line.ProductID]; !found {
					return domain.ErrInvalidRecordedBatch
				}
			}
			deliveries[delivery.DeliveryID] = struct{}{}
			for _, fact := range batch.Events[0].Payload.Batches {
				stateBatch, found := snapshot.State.Batches[fact.BatchID]
				if !found || !sameBatchReceiptFact(stateBatch, fact) {
					return domain.ErrInvalidSnapshot
				}
				if _, found := products[fact.ProductID]; !found {
					return domain.ErrInvalidRecordedBatch
				}
				createdBatches[fact.BatchID] = struct{}{}
			}
		case "ConfirmOrder":
			order := batch.Events[0].Payload.Order
			if order == nil || !sameOrder(snapshot.State.Orders[order.OrderID], *order) {
				return domain.ErrInvalidSnapshot
			}
			for _, allocation := range order.Allocations {
				if _, found := createdBatches[allocation.BatchID]; !found {
					return domain.ErrInvalidSnapshot
				}
			}
			for _, position := range order.AcceptedPositions {
				if _, found := dishes[position.DishID]; !found {
					return domain.ErrInvalidRecordedBatch
				}
			}
		case "ExpireBatch":
		default:
			return domain.ErrInvalidRecordedBatch
		}
		for _, event := range batch.Events {
			if event.Type != domain.EventBatchExpired {
				continue
			}
			expiry := event.Payload.Expiry
			if expiry == nil || expiry.WrittenOffQuantity <= 0 || expiry.Reason != domain.ExpiryReasonExpiration || !isSupportedBusinessTime(expiry.ProcessedAt) || expiry.ProcessedAt.Before(expiry.ExpiresAt) {
				return domain.ErrInvalidRecordedBatch
			}
			batchState, found := snapshot.State.Batches[expiry.BatchID]
			if !found || batchState.ProductID != expiry.ProductID || !batchState.ExpiresAt.Equal(expiry.ExpiresAt) {
				return domain.ErrInvalidSnapshot
			}
			if _, found := createdBatches[expiry.BatchID]; !found {
				return domain.ErrInvalidRecordedBatch
			}
			if batch.CommandType == "ExpireBatch" && !sameExpiry(*record.Result.Expiry, *expiry) {
				return domain.ErrInvalidSnapshot
			}
			if batch.CommandType == "ReceiveDelivery" {
				if _, found := deliveryBatchID(batch.Events[0].Payload.Batches, expiry.BatchID); !found {
					return domain.ErrInvalidSnapshot
				}
			}
			updated, err := domain.AddQuantity(expired[expiry.BatchID], expiry.WrittenOffQuantity)
			if err != nil {
				return domain.ErrInvalidSnapshot
			}
			expired[expiry.BatchID] = updated
		}
	}
	if len(products) != len(snapshot.State.Products) || len(dishes) != len(snapshot.State.Dishes) || len(deliveries) != len(snapshot.State.Deliveries) || len(createdBatches) != len(snapshot.State.Batches) {
		return domain.ErrInvalidSnapshot
	}
	for id, batch := range snapshot.State.Batches {
		if expired[id] != batch.WrittenOffForExpiry {
			return domain.ErrInvalidSnapshot
		}
	}
	return nil
}

func isSupportedBusinessTime(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && offset == 3*60*60
}

func sameCommandResult(a, b domain.CommandResult) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case domain.CommandResultProductRegistered:
		return a.Product != nil && b.Product != nil && sameProduct(*a.Product, *b.Product)
	case domain.CommandResultDishDefined:
		return a.Dish != nil && b.Dish != nil && sameDish(*a.Dish, *b.Dish)
	case domain.CommandResultDeliveryReceived:
		if a.Delivery == nil || b.Delivery == nil || a.Delivery.DeliveryID != b.Delivery.DeliveryID || len(a.Delivery.BatchIDs) != len(b.Delivery.BatchIDs) {
			return false
		}
		for i := range a.Delivery.BatchIDs {
			if a.Delivery.BatchIDs[i] != b.Delivery.BatchIDs[i] {
				return false
			}
		}
		return true
	case domain.CommandResultOrderPlaced:
		return a.Order != nil && b.Order != nil && sameOrder(*a.Order, *b.Order)
	case domain.CommandResultBatchExpired:
		return a.Expiry != nil && b.Expiry != nil && sameExpiry(*a.Expiry, *b.Expiry)
	default:
		return false
	}
}

func sameProduct(a, b domain.Product) bool {
	return a.ProductID == b.ProductID && a.Name == b.Name && a.BaseUnit == b.BaseUnit
}

func sameDish(a, b domain.Dish) bool {
	if a.DishID != b.DishID || a.Name != b.Name || len(a.Ingredients) != len(b.Ingredients) {
		return false
	}
	for i := range a.Ingredients {
		if a.Ingredients[i] != b.Ingredients[i] {
			return false
		}
	}
	return true
}

func sameDelivery(a, b domain.Delivery) bool {
	if a.DeliveryID != b.DeliveryID || a.Number != b.Number || !a.CreatedAt.Equal(b.CreatedAt) || len(a.Lines) != len(b.Lines) {
		return false
	}
	for i := range a.Lines {
		x, y := a.Lines[i], b.Lines[i]
		if x.ProductID != y.ProductID || !x.ManufacturedAt.Equal(y.ManufacturedAt) || x.ShelfLife != y.ShelfLife || !x.ReceivedAt.Equal(y.ReceivedAt) || x.Quantity != y.Quantity {
			return false
		}
	}
	return true
}

func sameBatchReceiptFact(a, b domain.Batch) bool {
	return a.BatchID == b.BatchID && a.DeliveryID == b.DeliveryID && a.LineNo == b.LineNo && a.ProductID == b.ProductID && a.ManufacturedAt.Equal(b.ManufacturedAt) && a.ShelfLife == b.ShelfLife && a.ReceivedAt.Equal(b.ReceivedAt) && a.ExpiresAt.Equal(b.ExpiresAt) && a.ReceivedQuantity == b.ReceivedQuantity && b.RemainingQuantity == b.ReceivedQuantity && b.ConsumedByOrders == 0 && b.WrittenOffForExpiry == 0
}

func deliveryBatchID(batches []domain.Batch, id string) (domain.Batch, bool) {
	for _, batch := range batches {
		if batch.BatchID == id {
			return batch, true
		}
	}
	return domain.Batch{}, false
}

func sameOrder(a, b domain.Order) bool {
	if a.OrderID != b.OrderID || a.ProposalID != b.ProposalID || !a.PlacedAt.Equal(b.PlacedAt) || len(a.AcceptedPositions) != len(b.AcceptedPositions) || len(a.Allocations) != len(b.Allocations) {
		return false
	}
	for i := range a.AcceptedPositions {
		if a.AcceptedPositions[i] != b.AcceptedPositions[i] {
			return false
		}
	}
	for i := range a.Allocations {
		if a.Allocations[i] != b.Allocations[i] {
			return false
		}
	}
	return true
}

func sameExpiry(a, b domain.ExpiryResult) bool {
	return a.BatchID == b.BatchID && a.ProductID == b.ProductID && a.WrittenOffQuantity == b.WrittenOffQuantity && a.Reason == b.Reason && a.ExpiresAt.Equal(b.ExpiresAt) && a.ProcessedAt.Equal(b.ProcessedAt)
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
