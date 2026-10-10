package domain

import (
	"math"
	"time"
)

func ApplyRecordedBatch(state InventoryState, batch RecordedBatch) (InventoryState, error) {
	if len(batch.Events) == 0 {
		return InventoryState{}, ErrInvalidEventBatch
	}
	if batch.CommandID == "" || batch.CommandType == "" || batch.CommandFingerprint == "" {
		return InventoryState{}, ErrInvalidRecordedBatch
	}
	if state.StateVersion < 0 || state.StateVersion == math.MaxInt64 || int64(len(batch.Events)) > math.MaxInt64-state.StateVersion || batch.FinalSequence != batch.Events[len(batch.Events)-1].Sequence {
		return InventoryState{}, ErrInvalidRecordedBatch
	}
	next := CloneInventoryState(state)
	ensureStateMaps(&next)
	if _, exists := next.Commands[batch.CommandID]; exists {
		return InventoryState{}, ErrCommandIdentityConflict
	}
	expected := next.StateVersion + 1
	for _, event := range batch.Events {
		if err := ValidateRecordedEvent(event, batch, expected); err != nil {
			return InventoryState{}, err
		}
		if err := applyEvent(&next, event); err != nil {
			return InventoryState{}, err
		}
		next.StateVersion = event.Sequence
		expected++
	}
	result, err := ResultForBatch(batch)
	if err != nil {
		return InventoryState{}, err
	}
	next.Commands[batch.CommandID] = CommandRecord{
		CommandID:          batch.CommandID,
		CommandType:        batch.CommandType,
		CommandFingerprint: batch.CommandFingerprint,
		Result:             result,
	}
	return next, nil
}

func ResultForBatch(batch RecordedBatch) (CommandResult, error) {
	if len(batch.Events) == 0 {
		return CommandResult{}, ErrInvalidEventBatch
	}
	switch batch.CommandType {
	case "RegisterProduct":
		if len(batch.Events) != 1 || batch.Events[0].Type != EventProductRegistered || batch.Events[0].Payload.Product == nil {
			return CommandResult{}, ErrInvalidEventBatch
		}
		product := *batch.Events[0].Payload.Product
		return CommandResult{Kind: CommandResultProductRegistered, Product: &product}, nil
	case "DefineDish":
		if len(batch.Events) != 1 || batch.Events[0].Type != EventDishDefined || batch.Events[0].Payload.Dish == nil {
			return CommandResult{}, ErrInvalidEventBatch
		}
		dish := CloneDish(*batch.Events[0].Payload.Dish)
		return CommandResult{Kind: CommandResultDishDefined, Dish: &dish}, nil
	case "ReceiveDelivery":
		if batch.Events[0].Type != EventDeliveryReceived || batch.Events[0].Payload.Delivery == nil || batch.Events[0].Payload.Delivery.DeliveryID == "" {
			return CommandResult{}, ErrInvalidEventBatch
		}
		delivery := batch.Events[0].Payload.Delivery
		batchIDs := make([]string, 0, len(batch.Events[0].Payload.Batches))
		seen := map[string]struct{}{}
		for _, registered := range batch.Events[0].Payload.Batches {
			if registered.BatchID == "" || registered.DeliveryID != delivery.DeliveryID {
				return CommandResult{}, ErrInvalidEventBatch
			}
			if _, ok := seen[registered.BatchID]; ok {
				return CommandResult{}, ErrInvalidEventBatch
			}
			seen[registered.BatchID] = struct{}{}
			batchIDs = append(batchIDs, registered.BatchID)
		}
		if len(batchIDs) == 0 || len(delivery.Lines) != len(batchIDs) {
			return CommandResult{}, ErrInvalidEventBatch
		}
		for _, event := range batch.Events[1:] {
			if event.Type != EventBatchExpired || event.Payload.Expiry == nil {
				return CommandResult{}, ErrInvalidEventBatch
			}
			if _, ok := seen[event.Payload.Expiry.BatchID]; !ok {
				return CommandResult{}, ErrInvalidEventBatch
			}
		}
		return CommandResult{Kind: CommandResultDeliveryReceived, Delivery: &DeliveryReceipt{DeliveryID: delivery.DeliveryID, BatchIDs: batchIDs}}, nil
	case "ConfirmOrder":
		if len(batch.Events) != 1 || batch.Events[0].Type != EventOrderPlaced || batch.Events[0].Payload.Order == nil || batch.Events[0].Payload.Order.OrderID == "" || batch.Events[0].Payload.Order.ProposalID == "" {
			return CommandResult{}, ErrInvalidEventBatch
		}
		order := CloneOrder(*batch.Events[0].Payload.Order)
		return CommandResult{Kind: CommandResultOrderPlaced, Order: &order}, nil
	case "ExpireBatch":
		if len(batch.Events) != 1 || batch.Events[0].Type != EventBatchExpired || batch.Events[0].Payload.Expiry == nil {
			return CommandResult{}, ErrInvalidEventBatch
		}
		expiry := CloneExpiryResult(*batch.Events[0].Payload.Expiry)
		return CommandResult{Kind: CommandResultBatchExpired, Expiry: &expiry}, nil
	default:
		return CommandResult{}, ErrInvalidEventBatch
	}
}

func ValidateRecordedEvent(event Event, batch RecordedBatch, expectedSequence int64) error {
	if event.SchemaVersion != InventorySchemaV1 {
		return ErrUnsupportedSchema
	}
	if event.EventID == "" || event.StreamID != InventoryStreamID || event.Sequence != expectedSequence || event.CommandID != batch.CommandID || event.CommandFingerprint != batch.CommandFingerprint || event.RecordedAt.IsZero() {
		return ErrInvalidRecordedBatch
	}
	if !isBusinessTime(event.RecordedAt) {
		return ErrInvalidBusinessTime
	}
	payloadCount := 0
	for _, present := range []bool{event.Payload.Product != nil, event.Payload.Dish != nil, event.Payload.Delivery != nil, event.Payload.Order != nil, event.Payload.Expiry != nil} {
		if present {
			payloadCount++
		}
	}
	if payloadCount != 1 || !validEventPayload(event) {
		return ErrInvalidEvent
	}
	switch event.Type {
	case EventProductRegistered, EventDishDefined, EventDeliveryReceived, EventOrderPlaced, EventBatchExpired:
		return nil
	default:
		return ErrInvalidEvent
	}
}

func validEventPayload(event Event) bool {
	switch event.Type {
	case EventProductRegistered:
		return event.Payload.Product != nil && event.Payload.Dish == nil && event.Payload.Delivery == nil && len(event.Payload.Batches) == 0 && event.Payload.Order == nil && event.Payload.Expiry == nil
	case EventDishDefined:
		return event.Payload.Product == nil && event.Payload.Dish != nil && event.Payload.Delivery == nil && len(event.Payload.Batches) == 0 && event.Payload.Order == nil && event.Payload.Expiry == nil
	case EventDeliveryReceived:
		return event.Payload.Product == nil && event.Payload.Dish == nil && event.Payload.Delivery != nil && event.Payload.Order == nil && event.Payload.Expiry == nil
	case EventOrderPlaced:
		return event.Payload.Product == nil && event.Payload.Dish == nil && event.Payload.Delivery == nil && len(event.Payload.Batches) == 0 && event.Payload.Order != nil && event.Payload.Expiry == nil
	case EventBatchExpired:
		return event.Payload.Product == nil && event.Payload.Dish == nil && event.Payload.Delivery == nil && len(event.Payload.Batches) == 0 && event.Payload.Order == nil && event.Payload.Expiry != nil
	default:
		return false
	}
}

func applyEvent(state *InventoryState, event Event) error {
	switch event.Type {
	case EventProductRegistered:
		product := *event.Payload.Product
		if product.ProductID == "" || product.Name == "" || !validUnit(product.BaseUnit) {
			return ErrInvalidEvent
		}
		if _, exists := state.Products[product.ProductID]; exists {
			return ErrInvalidEvent
		}
		state.Products[product.ProductID] = product
	case EventDishDefined:
		dish := CloneDish(*event.Payload.Dish)
		if dish.DishID == "" || dish.Name == "" || len(dish.Ingredients) == 0 {
			return ErrInvalidEvent
		}
		if _, exists := state.Dishes[dish.DishID]; exists {
			return ErrInvalidEvent
		}
		for _, ingredient := range dish.Ingredients {
			if _, exists := state.Products[ingredient.ProductID]; !exists {
				return ErrProductNotFound
			}
			if ingredient.Quantity <= 0 {
				return ErrInvalidEvent
			}
		}
		state.Dishes[dish.DishID] = dish
	case EventDeliveryReceived:
		return applyDelivery(state, event.Payload.Delivery, event.Payload.Batches)
	case EventOrderPlaced:
		return applyOrder(state, event.Payload.Order)
	case EventBatchExpired:
		return applyExpiry(state, event.Payload.Expiry)
	default:
		return ErrInvalidEvent
	}
	return nil
}

func applyDelivery(state *InventoryState, delivery *Delivery, batches []Batch) error {
	if delivery == nil || delivery.DeliveryID == "" || delivery.Number == "" || !isBusinessTime(delivery.CreatedAt) || len(delivery.Lines) == 0 || len(delivery.Lines) != len(batches) {
		return ErrInvalidEvent
	}
	if _, exists := state.Deliveries[delivery.DeliveryID]; exists {
		return ErrInvalidEvent
	}
	for i, registered := range batches {
		line := delivery.Lines[i]
		if registered.BatchID == "" || registered.DeliveryID != delivery.DeliveryID || registered.LineNo != int64(i+1) || registered.ProductID != line.ProductID || !registered.ManufacturedAt.Equal(line.ManufacturedAt) || !registered.ReceivedAt.Equal(line.ReceivedAt) || registered.ShelfLife != line.ShelfLife || registered.ReceivedQuantity != line.Quantity || registered.RemainingQuantity != line.Quantity || registered.ConsumedByOrders != 0 || registered.WrittenOffForExpiry != 0 || line.Quantity <= 0 {
			return ErrInvalidEvent
		}
		if !isBusinessTime(registered.ManufacturedAt) || !isBusinessTime(registered.ReceivedAt) || !isBusinessTime(registered.ExpiresAt) || registered.ShelfLife.Value <= 0 || registered.ShelfLife.Unit != ShelfLifeDay && registered.ShelfLife.Unit != ShelfLifeMonth || !registered.ExpiresAt.After(registered.ManufacturedAt) {
			return ErrInvalidEvent
		}
		if _, exists := state.Products[registered.ProductID]; !exists {
			return ErrProductNotFound
		}
		if registered.ExpiresAt.IsZero() || registered.BatchID == "" || !validBatchBalance(registered) {
			return ErrInvalidEvent
		}
		if _, exists := state.Batches[registered.BatchID]; exists {
			return ErrInvalidEvent
		}
	}
	state.Deliveries[delivery.DeliveryID] = CloneDelivery(*delivery)
	for _, batch := range batches {
		state.Batches[batch.BatchID] = batch
	}
	return nil
}

func applyOrder(state *InventoryState, order *Order) error {
	if order == nil || order.OrderID == "" || order.ProposalID == "" || order.PlacedAt.IsZero() || !isBusinessTime(order.PlacedAt) || len(order.AcceptedPositions) == 0 {
		return ErrInvalidEvent
	}
	if _, exists := state.Orders[order.OrderID]; exists {
		return ErrInvalidEvent
	}
	if _, exists := state.OrdersByProposal[order.ProposalID]; exists {
		return ErrInvalidEvent
	}
	required := map[string]Quantity{}
	for _, position := range order.AcceptedPositions {
		if position.LineNo <= 0 || position.Portions <= 0 {
			return ErrInvalidEvent
		}
		dish, exists := state.Dishes[position.DishID]
		if !exists {
			return ErrDishNotFound
		}
		for _, ingredient := range dish.Ingredients {
			quantity, err := MultiplyQuantity(ingredient.Quantity, position.Portions)
			if err != nil {
				return err
			}
			required[ingredient.ProductID], err = AddQuantity(required[ingredient.ProductID], quantity)
			if err != nil {
				return err
			}
		}
	}
	allocated := map[string]Quantity{}
	for _, allocation := range order.Allocations {
		if allocation.Quantity <= 0 {
			return ErrInvalidEvent
		}
		batch, exists := state.Batches[allocation.BatchID]
		if !exists {
			return ErrBatchNotFound
		}
		if batch.ProductID != allocation.ProductID || allocation.Quantity > batch.RemainingQuantity {
			return ErrInvalidEvent
		}
		var err error
		allocated[allocation.ProductID], err = AddQuantity(allocated[allocation.ProductID], allocation.Quantity)
		if err != nil {
			return err
		}
	}
	if len(required) != len(allocated) {
		return ErrInvalidEvent
	}
	for productID, quantity := range required {
		if allocated[productID] != quantity {
			return ErrInvalidEvent
		}
	}
	for _, allocation := range order.Allocations {
		batch := state.Batches[allocation.BatchID]
		remaining, err := AddQuantity(batch.RemainingQuantity, -allocation.Quantity)
		if err != nil || remaining < 0 {
			return ErrInvalidEvent
		}
		batch.RemainingQuantity = remaining
		batch.ConsumedByOrders, err = AddQuantity(batch.ConsumedByOrders, allocation.Quantity)
		if err != nil || !validBatchBalance(batch) {
			return ErrInvalidEvent
		}
		state.Batches[batch.BatchID] = batch
	}
	state.Orders[order.OrderID] = CloneOrder(*order)
	state.OrdersByProposal[order.ProposalID] = order.OrderID
	return nil
}

func applyExpiry(state *InventoryState, expiry *ExpiryResult) error {
	if expiry == nil || expiry.BatchID == "" || expiry.ProductID == "" || expiry.WrittenOffQuantity <= 0 || expiry.Reason != ExpiryReasonExpiration || expiry.ExpiresAt.IsZero() || expiry.ProcessedAt.IsZero() {
		return ErrInvalidEvent
	}
	batch, exists := state.Batches[expiry.BatchID]
	if !exists {
		return ErrBatchNotFound
	}
	if batch.ProductID != expiry.ProductID || !batch.ExpiresAt.Equal(expiry.ExpiresAt) || !isBusinessTime(expiry.ProcessedAt) || !isBusinessTime(expiry.ExpiresAt) || expiry.WrittenOffQuantity != batch.RemainingQuantity {
		return ErrInvalidEvent
	}
	batch.RemainingQuantity = 0
	updated, err := AddQuantity(batch.WrittenOffForExpiry, expiry.WrittenOffQuantity)
	if err != nil {
		return err
	}
	batch.WrittenOffForExpiry = updated
	if !validBatchBalance(batch) {
		return ErrInvalidEvent
	}
	state.Batches[batch.BatchID] = batch
	return nil
}

func validBatchBalance(batch Batch) bool {
	if batch.ReceivedQuantity < 0 || batch.RemainingQuantity < 0 || batch.ConsumedByOrders < 0 || batch.WrittenOffForExpiry < 0 {
		return false
	}
	total, err := AddQuantity(batch.RemainingQuantity, batch.ConsumedByOrders)
	if err != nil {
		return false
	}
	total, err = AddQuantity(total, batch.WrittenOffForExpiry)
	return err == nil && total == batch.ReceivedQuantity
}

func ensureStateMaps(state *InventoryState) {
	if state.Products == nil {
		state.Products = map[string]Product{}
	}
	if state.Dishes == nil {
		state.Dishes = map[string]Dish{}
	}
	if state.Deliveries == nil {
		state.Deliveries = map[string]Delivery{}
	}
	if state.Batches == nil {
		state.Batches = map[string]Batch{}
	}
	if state.Orders == nil {
		state.Orders = map[string]Order{}
	}
	if state.OrdersByProposal == nil {
		state.OrdersByProposal = map[string]string{}
	}
	if state.Commands == nil {
		state.Commands = map[string]CommandRecord{}
	}
}

func validUnit(unit BaseUnit) bool {
	return unit == BaseUnitG || unit == BaseUnitML || unit == BaseUnitPCS
}

func isBusinessTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 3*60*60
}
