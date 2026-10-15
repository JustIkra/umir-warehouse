package domain

import (
	"maps"
	"math"
	"time"
)

// StateDelta is the change layer one recorded batch makes to an InventoryState:
// only the records it created or replaced.
type StateDelta struct {
	Products     map[string]Product
	Dishes       map[string]Dish
	DeliveryIDs  map[string]struct{}
	Batches      map[string]Batch
	StateVersion int64
}

// PlanBatch validates the batch against state and returns the change it makes.
// It never modifies state; events see the changes of the earlier events of the
// same batch. On error nothing is returned.
func PlanBatch(state *InventoryState, batch RecordedBatch) (StateDelta, error) {
	if len(batch.Events) == 0 {
		return StateDelta{}, ErrInvalidEventBatch
	}
	if batch.CommandID == "" || batch.CommandType == "" || batch.CommandFingerprint == "" {
		return StateDelta{}, ErrInvalidRecordedBatch
	}
	if state.StateVersion < 0 || state.StateVersion == math.MaxInt64 || int64(len(batch.Events)) > math.MaxInt64-state.StateVersion || batch.FinalSequence != batch.Events[len(batch.Events)-1].Sequence {
		return StateDelta{}, ErrInvalidRecordedBatch
	}
	layer := stateLayer{base: state}
	expected := state.StateVersion + 1
	for _, event := range batch.Events {
		if err := ValidateRecordedEvent(event, batch, expected); err != nil {
			return StateDelta{}, err
		}
		if err := applyEvent(&layer, event); err != nil {
			return StateDelta{}, err
		}
		layer.delta.StateVersion = event.Sequence
		expected++
	}
	if _, err := ResultForBatch(batch); err != nil {
		return StateDelta{}, err
	}
	return layer.delta, nil
}

// Merge writes the delta into the state.
func (s *InventoryState) Merge(delta StateDelta) {
	if s.Products == nil {
		s.Products = map[string]Product{}
	}
	if s.Dishes == nil {
		s.Dishes = map[string]Dish{}
	}
	if s.DeliveryIDs == nil {
		s.DeliveryIDs = map[string]struct{}{}
	}
	if s.Batches == nil {
		s.Batches = map[string]Batch{}
	}
	maps.Copy(s.Products, delta.Products)
	maps.Copy(s.Dishes, delta.Dishes)
	maps.Copy(s.DeliveryIDs, delta.DeliveryIDs)
	maps.Copy(s.Batches, delta.Batches)
	s.StateVersion = delta.StateVersion
}

// ApplyBatch plans and merges one recorded batch; a rejected batch leaves the
// state untouched.
func (s *InventoryState) ApplyBatch(batch RecordedBatch) error {
	delta, err := PlanBatch(s, batch)
	if err != nil {
		return err
	}
	s.Merge(delta)
	return nil
}

// stateLayer reads through the delta first and the base state second, and
// writes only to the delta.
type stateLayer struct {
	base  *InventoryState
	delta StateDelta
}

func layered[V any](layer, base map[string]V, id string) (V, bool) {
	if value, ok := layer[id]; ok {
		return value, true
	}
	value, ok := base[id]
	return value, ok
}

func put[V any](layer *map[string]V, id string, value V) {
	if *layer == nil {
		*layer = map[string]V{}
	}
	(*layer)[id] = value
}

func (l *stateLayer) product(id string) (Product, bool) {
	return layered(l.delta.Products, l.base.Products, id)
}

func (l *stateLayer) dish(id string) (Dish, bool) {
	return layered(l.delta.Dishes, l.base.Dishes, id)
}

func (l *stateLayer) batch(id string) (Batch, bool) {
	return layered(l.delta.Batches, l.base.Batches, id)
}

func (l *stateLayer) hasDelivery(id string) bool {
	_, ok := layered(l.delta.DeliveryIDs, l.base.DeliveryIDs, id)
	return ok
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
			if (event.Type != EventBatchExpired || event.Payload.Expiry == nil) && (event.Type != EventBatchExpiryPending || event.Payload.Pending == nil) {
				return CommandResult{}, ErrInvalidEventBatch
			}
			id := ""
			if event.Payload.Expiry != nil {
				id = event.Payload.Expiry.BatchID
			}
			if event.Payload.Pending != nil {
				id = event.Payload.Pending.BatchID
			}
			if _, ok := seen[id]; !ok {
				return CommandResult{}, ErrInvalidEventBatch
			}
		}
		receipt := &DeliveryReceipt{DeliveryID: delivery.DeliveryID, BatchIDs: batchIDs}
		if batch.Events[0].SchemaVersion == InventorySchemaV2 {
			receipt.OrganizationID = delivery.OrganizationID
			receipt.RestaurantID = batch.Events[0].TenantID
			receipt.RuntimeEpoch = batch.Events[0].RuntimeEpoch
			receipt.LifecycleVersion = delivery.LifecycleVersion
			receipt.EvaluatedAt = batch.Events[0].RecordedAt
			receipt.StateVersion = batch.FinalSequence
			receipt.Batches = append([]Batch(nil), batch.Events[0].Payload.Batches...)
		}
		return CommandResult{Kind: CommandResultDeliveryReceived, Delivery: receipt}, nil
	case "ConfirmOrder":
		if len(batch.Events) != 1 || batch.Events[0].Type != EventOrderPlaced || batch.Events[0].Payload.Order == nil || batch.Events[0].Payload.Order.OrderID == "" || batch.Events[0].Payload.Order.ProposalID == "" {
			return CommandResult{}, ErrInvalidEventBatch
		}
		order := CloneOrder(*batch.Events[0].Payload.Order)
		return CommandResult{Kind: CommandResultOrderPlaced, Order: &order}, nil
	case "PrepareExpiry":
		if len(batch.Events) != 1 || batch.Events[0].Type != EventBatchExpiryPending || batch.Events[0].Payload.Pending == nil {
			return CommandResult{}, ErrInvalidEventBatch
		}
		p := *batch.Events[0].Payload.Pending
		return CommandResult{Kind: CommandResultBatchExpiryPending, Pending: &p}, nil
	case "ConfirmWriteoff":
		if len(batch.Events) != 1 || batch.Events[0].Type != EventBatchWriteoffConfirmed || batch.Events[0].Payload.Writeoff == nil {
			return CommandResult{}, ErrInvalidEventBatch
		}
		v := *batch.Events[0].Payload.Writeoff
		return CommandResult{Kind: CommandResultBatchWriteoffConfirmed, Writeoff: &v}, nil
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
	if event.SchemaVersion != InventorySchemaV1 && event.SchemaVersion != InventorySchemaV2 {
		return ErrUnsupportedSchema
	}
	if event.EventID == "" || event.StreamID != InventoryStreamID || event.Sequence != expectedSequence || event.CommandID != batch.CommandID || event.CommandFingerprint != batch.CommandFingerprint || event.RecordedAt.IsZero() {
		return ErrInvalidRecordedBatch
	}
	if !isBusinessTime(event.RecordedAt) {
		return ErrInvalidBusinessTime
	}
	newType := event.Type == EventBatchExpiryPending || event.Type == EventBatchWriteoffConfirmed
	v2Delivery := event.Type == EventDeliveryReceived && event.SchemaVersion == InventorySchemaV2
	if !v2Delivery && newType != (event.SchemaVersion == InventorySchemaV2) {
		return ErrUnsupportedSchema
	}
	if (newType || v2Delivery) && (event.TenantID == "" || event.RuntimeEpoch == "") {
		return ErrInvalidRecordedBatch
	}
	payloadCount := 0
	for _, present := range []bool{event.Payload.Product != nil, event.Payload.Dish != nil, event.Payload.Delivery != nil, event.Payload.Order != nil, event.Payload.Expiry != nil, event.Payload.Pending != nil, event.Payload.Writeoff != nil} {
		if present {
			payloadCount++
		}
	}
	if payloadCount != 1 || !validEventPayload(event) {
		return ErrInvalidEvent
	}
	switch event.Type {
	case EventProductRegistered, EventDishDefined, EventDeliveryReceived, EventOrderPlaced, EventBatchExpired, EventBatchExpiryPending, EventBatchWriteoffConfirmed:
		return nil
	default:
		return ErrInvalidEvent
	}
}

func validEventPayload(event Event) bool {
	if event.Type == EventBatchExpiryPending || event.Type == EventBatchWriteoffConfirmed {
		return event.Payload.Product == nil && event.Payload.Dish == nil && event.Payload.Delivery == nil && len(event.Payload.Batches) == 0 && event.Payload.Order == nil && event.Payload.Expiry == nil && ((event.Type == EventBatchExpiryPending && event.Payload.Pending != nil && event.Payload.Writeoff == nil) || (event.Type == EventBatchWriteoffConfirmed && event.Payload.Writeoff != nil && event.Payload.Pending == nil))
	}
	if event.Payload.Pending != nil || event.Payload.Writeoff != nil {
		return false
	}
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

func applyEvent(state *stateLayer, event Event) error {
	switch event.Type {
	case EventProductRegistered:
		product := *event.Payload.Product
		if product.ProductID == "" || product.Name == "" || !validUnit(product.BaseUnit) {
			return ErrInvalidEvent
		}
		if _, exists := state.product(product.ProductID); exists {
			return ErrInvalidEvent
		}
		put(&state.delta.Products, product.ProductID, product)
	case EventDishDefined:
		dish := CloneDish(*event.Payload.Dish)
		if dish.DishID == "" || dish.Name == "" || len(dish.Ingredients) == 0 {
			return ErrInvalidEvent
		}
		if _, exists := state.dish(dish.DishID); exists {
			return ErrInvalidEvent
		}
		ingredientProducts := make(map[string]struct{}, len(dish.Ingredients))
		for _, ingredient := range dish.Ingredients {
			if _, exists := ingredientProducts[ingredient.ProductID]; exists {
				return ErrInvalidEvent
			}
			ingredientProducts[ingredient.ProductID] = struct{}{}
			if _, exists := state.product(ingredient.ProductID); !exists {
				return ErrProductNotFound
			}
			if ingredient.Quantity <= 0 {
				return ErrInvalidEvent
			}
		}
		put(&state.delta.Dishes, dish.DishID, dish)
	case EventDeliveryReceived:
		return applyDelivery(state, event.Payload.Delivery, event.Payload.Batches)
	case EventOrderPlaced:
		return applyOrder(state, event.Payload.Order)
	case EventBatchExpiryPending:
		return applyPending(state, event)
	case EventBatchWriteoffConfirmed:
		return applyWriteoff(state, event)
	case EventBatchExpired:
		return applyExpiry(state, event.Payload.Expiry)
	default:
		return ErrInvalidEvent
	}
	return nil
}

func applyDelivery(state *stateLayer, delivery *Delivery, batches []Batch) error {
	if delivery == nil || delivery.DeliveryID == "" || delivery.Number == "" || !isBusinessTime(delivery.CreatedAt) || len(delivery.Lines) == 0 || len(delivery.Lines) != len(batches) {
		return ErrInvalidEvent
	}
	if state.hasDelivery(delivery.DeliveryID) {
		return ErrInvalidEvent
	}
	for i, registered := range batches {
		line := delivery.Lines[i]
		if registered.BatchID == "" || registered.DeliveryID != delivery.DeliveryID || registered.LineNo != int64(i+1) || registered.ProductID != line.ProductID || !registered.ManufacturedAt.Equal(line.ManufacturedAt) || !registered.ReceivedAt.Equal(line.ReceivedAt) || registered.ShelfLife != line.ShelfLife || registered.ReceivedQuantity != line.Quantity || registered.RemainingQuantity != line.Quantity || registered.ConsumedByOrders != 0 || registered.WrittenOffForExpiry != 0 || registered.PendingWriteoff || line.Quantity <= 0 {
			return ErrInvalidEvent
		}
		if !isBusinessTime(registered.ManufacturedAt) || !isBusinessTime(registered.ReceivedAt) || !isBusinessTime(registered.ExpiresAt) || registered.ShelfLife.Value <= 0 || registered.ShelfLife.Unit != ShelfLifeDay && registered.ShelfLife.Unit != ShelfLifeMonth || !registered.ExpiresAt.After(registered.ManufacturedAt) {
			return ErrInvalidEvent
		}
		if _, exists := state.product(registered.ProductID); !exists {
			return ErrProductNotFound
		}
		if registered.ExpiresAt.IsZero() || registered.BatchID == "" || !validBatchBalance(registered) {
			return ErrInvalidEvent
		}
		if _, exists := state.batch(registered.BatchID); exists {
			return ErrInvalidEvent
		}
	}
	put(&state.delta.DeliveryIDs, delivery.DeliveryID, struct{}{})
	for _, batch := range batches {
		put(&state.delta.Batches, batch.BatchID, batch)
	}
	return nil
}

func applyOrder(state *stateLayer, order *Order) error {
	if order == nil || order.OrderID == "" || order.ProposalID == "" || order.PlacedAt.IsZero() || !isBusinessTime(order.PlacedAt) || len(order.AcceptedPositions) == 0 {
		return ErrInvalidEvent
	}
	required := map[string]Quantity{}
	for _, position := range order.AcceptedPositions {
		if position.LineNo <= 0 || position.Portions <= 0 {
			return ErrInvalidEvent
		}
		dish, exists := state.dish(position.DishID)
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
		batch, exists := state.batch(allocation.BatchID)
		if !exists {
			return ErrBatchNotFound
		}
		if batch.PendingWriteoff || batch.ProductID != allocation.ProductID || allocation.Quantity > batch.RemainingQuantity {
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
		batch, _ := state.batch(allocation.BatchID)
		remaining, err := AddQuantity(batch.RemainingQuantity, -allocation.Quantity)
		if err != nil || remaining < 0 {
			return ErrInvalidEvent
		}
		batch.RemainingQuantity = remaining
		batch.ConsumedByOrders, err = AddQuantity(batch.ConsumedByOrders, allocation.Quantity)
		if err != nil || !validBatchBalance(batch) {
			return ErrInvalidEvent
		}
		put(&state.delta.Batches, batch.BatchID, batch)
	}
	return nil
}

func applyExpiry(state *stateLayer, expiry *ExpiryResult) error {
	if expiry == nil || expiry.BatchID == "" || expiry.ProductID == "" || expiry.WrittenOffQuantity <= 0 || expiry.Reason != ExpiryReasonExpiration || expiry.ExpiresAt.IsZero() || expiry.ProcessedAt.IsZero() {
		return ErrInvalidEvent
	}
	batch, exists := state.batch(expiry.BatchID)
	if !exists {
		return ErrBatchNotFound
	}
	if batch.ProductID != expiry.ProductID || !batch.ExpiresAt.Equal(expiry.ExpiresAt) || !isBusinessTime(expiry.ProcessedAt) || !isBusinessTime(expiry.ExpiresAt) || expiry.WrittenOffQuantity != batch.RemainingQuantity {
		return ErrInvalidEvent
	}
	batch.PendingWriteoff = false
	batch.RemainingQuantity = 0
	updated, err := AddQuantity(batch.WrittenOffForExpiry, expiry.WrittenOffQuantity)
	if err != nil {
		return err
	}
	batch.WrittenOffForExpiry = updated
	if !validBatchBalance(batch) {
		return ErrInvalidEvent
	}
	put(&state.delta.Batches, batch.BatchID, batch)
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

func applyPending(state *stateLayer, event Event) error {
	p := event.Payload.Pending
	b, ok := state.batch(p.BatchID)
	product, _ := state.product(p.ProductID)
	if !ok || b.PendingWriteoff || b.RemainingQuantity <= 0 || b.ProductID != p.ProductID || b.RemainingQuantity != p.RemainingQuantity || product.BaseUnit != p.BaseUnit || !b.ExpiresAt.Equal(p.ExpiresAt) || event.RecordedAt.Before(p.ExpiresAt) {
		return ErrInvalidEvent
	}
	b.PendingWriteoff = true
	put(&state.delta.Batches, b.BatchID, b)
	return nil
}
func applyWriteoff(state *stateLayer, event Event) error {
	w := event.Payload.Writeoff
	b, ok := state.batch(w.BatchID)
	product, _ := state.product(b.ProductID)
	if !ok || !b.PendingWriteoff || w.ProductID != b.ProductID || w.WrittenOffQuantity != b.RemainingQuantity || w.WrittenOffQuantity <= 0 || w.BaseUnit != product.BaseUnit || w.ActorUserID == "" || w.EventID != event.EventID || !w.ConfirmedAt.Equal(event.RecordedAt) || event.RecordedAt.Before(b.ExpiresAt) || w.RestaurantID != event.TenantID || w.RuntimeEpoch != event.RuntimeEpoch || w.StateVersion != event.Sequence || w.OrganizationID == "" || w.TimeZone == "" || w.SettingsVersion == "" {
		return ErrInvalidEvent
	}
	b.RemainingQuantity = 0
	b.PendingWriteoff = false
	var err error
	b.WrittenOffForExpiry, err = AddQuantity(b.WrittenOffForExpiry, w.WrittenOffQuantity)
	if err != nil || !validBatchBalance(b) {
		return ErrInvalidEvent
	}
	put(&state.delta.Batches, b.BatchID, b)
	return nil
}
