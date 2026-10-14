package domain

import (
	"errors"
	"fmt"
	"slices"
)

var ErrInvalidSnapshot = errors.New("invalid snapshot")

func ValidateSnapshot(snapshot Snapshot) error {
	if snapshot.SchemaVersion != InventorySchemaV1 {
		return ErrUnsupportedSchema
	}
	if snapshot.StreamID != InventoryStreamID || snapshot.LastSequence < 0 || snapshot.State.StateVersion != snapshot.LastSequence {
		return ErrInvalidSnapshot
	}
	return ValidateInventoryState(snapshot.State)
}

func ValidateSnapshotPrefix(snapshot Snapshot, batches []RecordedBatch) error {
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	state := InventoryState{}
	boundary := snapshot.LastSequence == 0
	for _, batch := range batches {
		if batch.FinalSequence > snapshot.LastSequence {
			break
		}
		next, err := ApplyRecordedBatch(state, batch)
		if err != nil {
			return err
		}
		state = next
		boundary = batch.FinalSequence == snapshot.LastSequence
	}
	if !boundary || state.StateVersion != snapshot.LastSequence || !equalInventoryState(state, snapshot.State) {
		return ErrInvalidSnapshot
	}
	return nil
}

func equalInventoryState(a, b InventoryState) bool {
	if a.StateVersion != b.StateVersion || len(a.Products) != len(b.Products) || len(a.Dishes) != len(b.Dishes) || len(a.Deliveries) != len(b.Deliveries) || len(a.Batches) != len(b.Batches) || len(a.Orders) != len(b.Orders) || len(a.OrdersByProposal) != len(b.OrdersByProposal) || len(a.Commands) != len(b.Commands) {
		return false
	}
	for id, value := range a.Products {
		other, exists := b.Products[id]
		if !exists || !equalProduct(value, other) {
			return false
		}
	}
	for id, value := range a.Dishes {
		other, exists := b.Dishes[id]
		if !exists || !equalDish(value, other) {
			return false
		}
	}
	for id, value := range a.Deliveries {
		other, exists := b.Deliveries[id]
		if !exists || !equalDelivery(value, other) {
			return false
		}
	}
	for id, value := range a.Batches {
		other, exists := b.Batches[id]
		if !exists || !equalBatch(value, other) {
			return false
		}
	}
	for id, value := range a.Orders {
		other, exists := b.Orders[id]
		if !exists || !equalOrder(value, other) {
			return false
		}
	}
	for id, value := range a.OrdersByProposal {
		if other, exists := b.OrdersByProposal[id]; !exists || other != value {
			return false
		}
	}
	for id, value := range a.Commands {
		other, exists := b.Commands[id]
		if !exists || value.CommandID != other.CommandID || value.CommandType != other.CommandType || value.CommandFingerprint != other.CommandFingerprint || !equalCommandResult(value.Result, other.Result) {
			return false
		}
	}
	return true
}

func equalDelivery(a, b Delivery) bool {
	if a.OrganizationID != b.OrganizationID || a.LifecycleVersion != b.LifecycleVersion || a.DeliveryID != b.DeliveryID || a.Number != b.Number || !a.CreatedAt.Equal(b.CreatedAt) || len(a.Lines) != len(b.Lines) {
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

func equalBatch(a, b Batch) bool {
	return a.BatchID == b.BatchID && a.DeliveryID == b.DeliveryID && a.LineNo == b.LineNo && a.ProductID == b.ProductID && a.ManufacturedAt.Equal(b.ManufacturedAt) && a.ShelfLife == b.ShelfLife && a.ReceivedAt.Equal(b.ReceivedAt) && a.ExpiresAt.Equal(b.ExpiresAt) && a.ReceivedQuantity == b.ReceivedQuantity && a.RemainingQuantity == b.RemainingQuantity && a.ConsumedByOrders == b.ConsumedByOrders && a.WrittenOffForExpiry == b.WrittenOffForExpiry && a.PendingWriteoff == b.PendingWriteoff && a.AppliedTimeZone == b.AppliedTimeZone && a.AppliedSettingsVersion == b.AppliedSettingsVersion && a.TZDataVersion == b.TZDataVersion
}

func equalCommandResult(a, b CommandResult) bool {
	if a.Kind != b.Kind || (a.Product == nil) != (b.Product == nil) || (a.Dish == nil) != (b.Dish == nil) || (a.Delivery == nil) != (b.Delivery == nil) || (a.Order == nil) != (b.Order == nil) || (a.Expiry == nil) != (b.Expiry == nil) || (a.Pending == nil) != (b.Pending == nil) || (a.Writeoff == nil) != (b.Writeoff == nil) {
		return false
	}
	if a.Product != nil && !equalProduct(*a.Product, *b.Product) || a.Dish != nil && !equalDish(*a.Dish, *b.Dish) || a.Order != nil && !equalOrder(*a.Order, *b.Order) {
		return false
	}
	if a.Delivery != nil {
		if a.Delivery.OrganizationID != b.Delivery.OrganizationID || a.Delivery.RestaurantID != b.Delivery.RestaurantID || a.Delivery.RuntimeEpoch != b.Delivery.RuntimeEpoch || a.Delivery.LifecycleVersion != b.Delivery.LifecycleVersion || !a.Delivery.EvaluatedAt.Equal(b.Delivery.EvaluatedAt) || a.Delivery.StateVersion != b.Delivery.StateVersion || len(a.Delivery.Batches) != len(b.Delivery.Batches) || a.Delivery.DeliveryID != b.Delivery.DeliveryID || len(a.Delivery.BatchIDs) != len(b.Delivery.BatchIDs) {
			return false
		}
		for i := range a.Delivery.Batches {
			if !equalBatch(a.Delivery.Batches[i], b.Delivery.Batches[i]) {
				return false
			}
		}
		for i := range a.Delivery.BatchIDs {
			if a.Delivery.BatchIDs[i] != b.Delivery.BatchIDs[i] {
				return false
			}
		}
	}
	if a.Pending != nil && *a.Pending != *b.Pending {
		return false
	}
	if a.Writeoff != nil && *a.Writeoff != *b.Writeoff {
		return false
	}
	return a.Expiry == nil || equalExpiry(*a.Expiry, *b.Expiry)
}

func equalExpiry(a, b ExpiryResult) bool {
	return a.BatchID == b.BatchID && a.ProductID == b.ProductID && a.WrittenOffQuantity == b.WrittenOffQuantity && a.Reason == b.Reason && a.ExpiresAt.Equal(b.ExpiresAt) && a.ProcessedAt.Equal(b.ProcessedAt)
}

func ValidateInventoryState(state InventoryState) error {
	if state.StateVersion < 0 || state.StateVersion > 0 && len(state.Commands) == 0 {
		return ErrInvalidSnapshot
	}
	for id, product := range state.Products {
		if id == "" || id != product.ProductID || product.Name == "" || !validUnit(product.BaseUnit) {
			return ErrInvalidSnapshot
		}
	}
	for id, dish := range state.Dishes {
		if id == "" || id != dish.DishID || dish.Name == "" || len(dish.Ingredients) == 0 {
			return ErrInvalidSnapshot
		}
		seen := make(map[string]struct{}, len(dish.Ingredients))
		for _, ingredient := range dish.Ingredients {
			if ingredient.ProductID == "" || ingredient.Quantity <= 0 {
				return ErrInvalidSnapshot
			}
			if _, exists := state.Products[ingredient.ProductID]; !exists {
				return fmt.Errorf("%w: dish %q references missing product %q", ErrInvalidSnapshot, id, ingredient.ProductID)
			}
			if _, exists := seen[ingredient.ProductID]; exists {
				return ErrInvalidSnapshot
			}
			seen[ingredient.ProductID] = struct{}{}
		}
	}
	if err := validateDeliveriesAndBatches(state); err != nil {
		return err
	}
	if err := validateOrders(state); err != nil {
		return err
	}
	return validateCommands(state)
}

func validateDeliveriesAndBatches(state InventoryState) error {
	for id, delivery := range state.Deliveries {
		if id == "" || id != delivery.DeliveryID || delivery.Number == "" || len(delivery.Lines) == 0 || !isBusinessTime(delivery.CreatedAt) {
			return ErrInvalidSnapshot
		}
		for _, line := range delivery.Lines {
			if line.ProductID == "" || line.Quantity <= 0 || line.ShelfLife.Value <= 0 || line.ShelfLife.Unit != ShelfLifeDay && line.ShelfLife.Unit != ShelfLifeMonth || !isBusinessTime(line.ManufacturedAt) || !isBusinessTime(line.ReceivedAt) {
				return ErrInvalidSnapshot
			}
			if _, exists := state.Products[line.ProductID]; !exists {
				return fmt.Errorf("%w: delivery %q references missing product %q", ErrInvalidSnapshot, id, line.ProductID)
			}
		}
	}
	counts := make(map[string]int, len(state.Deliveries))
	for id, batch := range state.Batches {
		if id == "" || id != batch.BatchID || batch.DeliveryID == "" || batch.ProductID == "" || batch.LineNo <= 0 || batch.ReceivedQuantity <= 0 || batch.ShelfLife.Value <= 0 || batch.ShelfLife.Unit != ShelfLifeDay && batch.ShelfLife.Unit != ShelfLifeMonth || !isBusinessTime(batch.ManufacturedAt) || !isBusinessTime(batch.ReceivedAt) || !isBusinessTime(batch.ExpiresAt) || !batch.ExpiresAt.After(batch.ManufacturedAt) || batch.PendingWriteoff && (batch.RemainingQuantity <= 0 || batch.WrittenOffForExpiry > 0) || batch.WrittenOffForExpiry > 0 && batch.RemainingQuantity > 0 || !validBatchBalance(batch) {
			return ErrInvalidSnapshot
		}
		if _, exists := state.Products[batch.ProductID]; !exists {
			return fmt.Errorf("%w: batch %q references missing product %q", ErrInvalidSnapshot, id, batch.ProductID)
		}
		delivery, exists := state.Deliveries[batch.DeliveryID]
		if !exists || batch.LineNo > int64(len(delivery.Lines)) {
			return fmt.Errorf("%w: batch %q references missing delivery line", ErrInvalidSnapshot, id)
		}
		line := delivery.Lines[batch.LineNo-1]
		if line.ProductID != batch.ProductID || line.Quantity != batch.ReceivedQuantity || line.ShelfLife != batch.ShelfLife || !line.ManufacturedAt.Equal(batch.ManufacturedAt) || !line.ReceivedAt.Equal(batch.ReceivedAt) {
			return fmt.Errorf("%w: batch %q does not match its delivery line", ErrInvalidSnapshot, id)
		}
		counts[batch.DeliveryID]++
	}
	for id, delivery := range state.Deliveries {
		if counts[id] != len(delivery.Lines) {
			return fmt.Errorf("%w: delivery %q has incomplete batch inventory", ErrInvalidSnapshot, id)
		}
	}
	return nil
}

func validateOrders(state InventoryState) error {
	if len(state.OrdersByProposal) != len(state.Orders) {
		return ErrInvalidSnapshot
	}
	consumed := make(map[string]Quantity, len(state.Batches))
	for id, order := range state.Orders {
		if id == "" || id != order.OrderID || order.ProposalID == "" || len(order.AcceptedPositions) == 0 || !isBusinessTime(order.PlacedAt) {
			return ErrInvalidSnapshot
		}
		if state.OrdersByProposal[order.ProposalID] != id {
			return fmt.Errorf("%w: order %q is missing from proposal index", ErrInvalidSnapshot, id)
		}
		required := make(map[string]Quantity)
		for _, position := range order.AcceptedPositions {
			if position.LineNo <= 0 || position.Portions <= 0 {
				return ErrInvalidSnapshot
			}
			dish, exists := state.Dishes[position.DishID]
			if !exists {
				return fmt.Errorf("%w: order %q references missing dish %q", ErrInvalidSnapshot, id, position.DishID)
			}
			for _, ingredient := range dish.Ingredients {
				quantity, err := MultiplyQuantity(ingredient.Quantity, position.Portions)
				if err != nil {
					return ErrInvalidSnapshot
				}
				required[ingredient.ProductID], err = AddQuantity(required[ingredient.ProductID], quantity)
				if err != nil {
					return ErrInvalidSnapshot
				}
			}
		}
		allocated := make(map[string]Quantity)
		for _, allocation := range order.Allocations {
			batch, exists := state.Batches[allocation.BatchID]
			if allocation.Quantity <= 0 || !exists || batch.ProductID != allocation.ProductID {
				return fmt.Errorf("%w: order %q has invalid allocation", ErrInvalidSnapshot, id)
			}
			total, err := AddQuantity(consumed[allocation.BatchID], allocation.Quantity)
			if err != nil {
				return ErrInvalidSnapshot
			}
			consumed[allocation.BatchID] = total
			allocated[allocation.ProductID], err = AddQuantity(allocated[allocation.ProductID], allocation.Quantity)
			if err != nil {
				return ErrInvalidSnapshot
			}
		}
		if len(required) != len(allocated) {
			return fmt.Errorf("%w: order %q allocations do not match its recipes", ErrInvalidSnapshot, id)
		}
		for productID, quantity := range required {
			if allocated[productID] != quantity {
				return fmt.Errorf("%w: order %q allocations do not match its recipes", ErrInvalidSnapshot, id)
			}
		}
	}
	for proposalID, orderID := range state.OrdersByProposal {
		order, exists := state.Orders[orderID]
		if proposalID == "" || !exists || order.ProposalID != proposalID {
			return ErrInvalidSnapshot
		}
	}
	for id, batch := range state.Batches {
		if consumed[id] != batch.ConsumedByOrders {
			return fmt.Errorf("%w: batch %q consumption does not match recorded orders", ErrInvalidSnapshot, id)
		}
	}
	return nil
}

func validateCommands(state InventoryState) error {
	products := make(map[string]struct{}, len(state.Products))
	dishes := make(map[string]struct{}, len(state.Dishes))
	deliveries := make(map[string]struct{}, len(state.Deliveries))
	orders := make(map[string]struct{}, len(state.Orders))
	pending := make(map[string]PendingResult)
	confirmed := make(map[string]WriteoffResult)
	expired := make(map[string]Quantity)
	deliveryBatches := make(map[string]struct{}, len(state.Batches))
	for id, record := range state.Commands {
		if id == "" || id != record.CommandID || record.CommandType == "" || record.CommandFingerprint == "" {
			return ErrInvalidSnapshot
		}
		payloads := 0
		for _, present := range []bool{record.Result.Product != nil, record.Result.Dish != nil, record.Result.Delivery != nil, record.Result.Order != nil, record.Result.Expiry != nil, record.Result.Pending != nil, record.Result.Writeoff != nil} {
			if present {
				payloads++
			}
		}
		if payloads != 1 {
			return ErrInvalidSnapshot
		}
		switch record.Result.Kind {
		case CommandResultProductRegistered:
			if record.CommandType != "RegisterProduct" || record.Result.Product == nil {
				return ErrInvalidSnapshot
			}
			productID := record.Result.Product.ProductID
			product, exists := state.Products[productID]
			if !exists || !equalProduct(product, *record.Result.Product) {
				return ErrInvalidSnapshot
			}
			if _, exists := products[productID]; exists {
				return ErrInvalidSnapshot
			}
			products[productID] = struct{}{}
		case CommandResultDishDefined:
			if record.CommandType != "DefineDish" || record.Result.Dish == nil {
				return ErrInvalidSnapshot
			}
			dishID := record.Result.Dish.DishID
			dish, exists := state.Dishes[dishID]
			if !exists || !equalDish(dish, *record.Result.Dish) {
				return ErrInvalidSnapshot
			}
			if _, exists := dishes[dishID]; exists {
				return ErrInvalidSnapshot
			}
			dishes[dishID] = struct{}{}
		case CommandResultDeliveryReceived:
			if err := validateDeliveryCommandResult(state, record); err != nil {
				return err
			}
			deliveryID := record.Result.Delivery.DeliveryID
			if _, exists := deliveries[deliveryID]; exists {
				return ErrInvalidSnapshot
			}
			deliveries[deliveryID] = struct{}{}
			for _, batchID := range record.Result.Delivery.BatchIDs {
				deliveryBatches[batchID] = struct{}{}
			}
		case CommandResultOrderPlaced:
			if record.CommandType != "ConfirmOrder" || record.Result.Order == nil {
				return ErrInvalidSnapshot
			}
			orderID := record.Result.Order.OrderID
			order, exists := state.Orders[orderID]
			if !exists || !equalOrder(order, *record.Result.Order) {
				return ErrInvalidSnapshot
			}
			if _, exists := orders[orderID]; exists {
				return ErrInvalidSnapshot
			}
			orders[orderID] = struct{}{}
		case CommandResultBatchExpiryPending:
			if record.CommandType != "PrepareExpiry" || record.Result.Pending == nil {
				return ErrInvalidSnapshot
			}
			p := *record.Result.Pending
			b, ok := state.Batches[p.BatchID]
			if !ok || p.ProductID != b.ProductID || p.RemainingQuantity <= 0 || p.BaseUnit != state.Products[b.ProductID].BaseUnit || !p.ExpiresAt.Equal(b.ExpiresAt) {
				return ErrInvalidSnapshot
			}
			if _, ok := pending[p.BatchID]; ok {
				return ErrInvalidSnapshot
			}
			pending[p.BatchID] = p
		case CommandResultBatchWriteoffConfirmed:
			if record.CommandType != "ConfirmWriteoff" || record.Result.Writeoff == nil {
				return ErrInvalidSnapshot
			}
			w := *record.Result.Writeoff
			b, ok := state.Batches[w.BatchID]
			if !ok || w.ProductID != b.ProductID || w.WrittenOffQuantity <= 0 || w.BaseUnit != state.Products[b.ProductID].BaseUnit || w.ActorUserID == "" || w.EventID == "" || !isBusinessTime(w.ConfirmedAt) || w.ConfirmedAt.Before(b.ExpiresAt) || w.OrganizationID == "" || w.RestaurantID == "" || w.RuntimeEpoch == "" || w.TimeZone == "" || w.SettingsVersion == "" || w.StateVersion <= 0 || w.StateVersion > state.StateVersion {
				return ErrInvalidSnapshot
			}
			if _, ok := confirmed[w.BatchID]; ok {
				return ErrInvalidSnapshot
			}
			confirmed[w.BatchID] = w
		case CommandResultBatchExpired:
			if record.CommandType != "ExpireBatch" || record.Result.Expiry == nil {
				return ErrInvalidSnapshot
			}
			expiry := record.Result.Expiry
			batch, exists := state.Batches[expiry.BatchID]
			if !exists || expiry.ProductID != batch.ProductID || expiry.Reason != ExpiryReasonExpiration || expiry.WrittenOffQuantity <= 0 || !expiry.ExpiresAt.Equal(batch.ExpiresAt) || !isBusinessTime(expiry.ProcessedAt) {
				return ErrInvalidSnapshot
			}
			if _, exists := expired[expiry.BatchID]; exists {
				return ErrInvalidSnapshot
			}
			total, err := AddQuantity(expired[expiry.BatchID], expiry.WrittenOffQuantity)
			if err != nil {
				return ErrInvalidSnapshot
			}
			expired[expiry.BatchID] = total
		default:
			return ErrInvalidSnapshot
		}
	}
	if len(products) != len(state.Products) || len(dishes) != len(state.Dishes) || len(deliveries) != len(state.Deliveries) || len(orders) != len(state.Orders) {
		return ErrInvalidSnapshot
	}
	for id := range state.Products {
		if _, exists := products[id]; !exists {
			return ErrInvalidSnapshot
		}
	}
	for id := range state.Dishes {
		if _, exists := dishes[id]; !exists {
			return ErrInvalidSnapshot
		}
	}
	for id := range state.Deliveries {
		if _, exists := deliveries[id]; !exists {
			return ErrInvalidSnapshot
		}
	}
	for id := range state.Orders {
		if _, exists := orders[id]; !exists {
			return ErrInvalidSnapshot
		}
	}
	for id, batch := range state.Batches {
		if w, ok := confirmed[id]; ok {
			if batch.PendingWriteoff || batch.RemainingQuantity != 0 || batch.WrittenOffForExpiry != w.WrittenOffQuantity || expired[id] > 0 {
				return ErrInvalidSnapshot
			}
			if p, ok := pending[id]; ok && p.RemainingQuantity != w.WrittenOffQuantity {
				return ErrInvalidSnapshot
			}
			continue
		}
		if p, ok := pending[id]; ok {
			if !batch.PendingWriteoff || batch.RemainingQuantity != p.RemainingQuantity {
				return ErrInvalidSnapshot
			}
		}
		if batch.PendingWriteoff {
			if _, ok := pending[id]; !ok {
				_, delivered := deliveryBatches[id]
				if !delivered || batch.RemainingQuantity != batch.ReceivedQuantity || batch.ConsumedByOrders != 0 {
					return ErrInvalidSnapshot
				}
			}
		}
		if expired[id] > 0 {
			if expired[id] != batch.WrittenOffForExpiry {
				return fmt.Errorf("%w: batch %q write-off does not match command results", ErrInvalidSnapshot, id)
			}
			continue
		}
		if batch.WrittenOffForExpiry == 0 {
			continue
		}
		_, hasDeliveryReceipt := deliveryBatches[id]
		if !hasDeliveryReceipt || batch.RemainingQuantity != 0 || batch.ConsumedByOrders != 0 || batch.WrittenOffForExpiry != batch.ReceivedQuantity {
			return fmt.Errorf("%w: batch %q write-off does not match command results", ErrInvalidSnapshot, id)
		}
	}
	return nil
}

func equalProduct(a, b Product) bool {
	return a.ProductID == b.ProductID && a.Name == b.Name && a.BaseUnit == b.BaseUnit
}

func equalDish(a, b Dish) bool {
	return a.DishID == b.DishID && a.Name == b.Name && slices.EqualFunc(a.Ingredients, b.Ingredients, func(x, y Ingredient) bool {
		return x.ProductID == y.ProductID && x.Quantity == y.Quantity
	})
}

func equalOrder(a, b Order) bool {
	return a.OrderID == b.OrderID && a.ProposalID == b.ProposalID && a.PlacedAt.Equal(b.PlacedAt) &&
		slices.EqualFunc(a.AcceptedPositions, b.AcceptedPositions, func(x, y AcceptedPosition) bool {
			return x.LineNo == y.LineNo && x.DishID == y.DishID && x.Portions == y.Portions
		}) && slices.EqualFunc(a.Allocations, b.Allocations, func(x, y Allocation) bool {
		return x.ProductID == y.ProductID && x.BatchID == y.BatchID && x.Quantity == y.Quantity
	})
}

func validateDeliveryCommandResult(state InventoryState, record CommandRecord) error {
	if record.CommandType != "ReceiveDelivery" || record.Result.Delivery == nil {
		return ErrInvalidSnapshot
	}
	deliveryID := record.Result.Delivery.DeliveryID
	delivery, exists := state.Deliveries[deliveryID]
	if !exists || len(record.Result.Delivery.BatchIDs) != len(delivery.Lines) {
		return ErrInvalidSnapshot
	}
	for index, batchID := range record.Result.Delivery.BatchIDs {
		batch, exists := state.Batches[batchID]
		if !exists || batch.DeliveryID != deliveryID || batch.LineNo != int64(index+1) {
			return ErrInvalidSnapshot
		}
	}
	return nil
}
