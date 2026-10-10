package domain

import (
	"errors"
	"fmt"
	"reflect"
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
		if id == "" || id != batch.BatchID || batch.DeliveryID == "" || batch.ProductID == "" || batch.LineNo <= 0 || batch.ReceivedQuantity <= 0 || batch.ShelfLife.Value <= 0 || batch.ShelfLife.Unit != ShelfLifeDay && batch.ShelfLife.Unit != ShelfLifeMonth || !isBusinessTime(batch.ManufacturedAt) || !isBusinessTime(batch.ReceivedAt) || !isBusinessTime(batch.ExpiresAt) || !batch.ExpiresAt.After(batch.ManufacturedAt) || !validBatchBalance(batch) {
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
	productResults := 0
	dishResults := 0
	deliveryResults := 0
	orderResults := 0
	expired := make(map[string]Quantity)
	for id, record := range state.Commands {
		if id == "" || id != record.CommandID || record.CommandType == "" || record.CommandFingerprint == "" {
			return ErrInvalidSnapshot
		}
		payloads := 0
		for _, present := range []bool{record.Result.Product != nil, record.Result.Dish != nil, record.Result.Delivery != nil, record.Result.Order != nil, record.Result.Expiry != nil} {
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
			product, exists := state.Products[record.Result.Product.ProductID]
			if !exists || !reflect.DeepEqual(product, *record.Result.Product) {
				return ErrInvalidSnapshot
			}
			productResults++
		case CommandResultDishDefined:
			if record.CommandType != "DefineDish" || record.Result.Dish == nil {
				return ErrInvalidSnapshot
			}
			dish, exists := state.Dishes[record.Result.Dish.DishID]
			if !exists || !reflect.DeepEqual(dish, *record.Result.Dish) {
				return ErrInvalidSnapshot
			}
			dishResults++
		case CommandResultDeliveryReceived:
			if err := validateDeliveryCommandResult(state, record); err != nil {
				return err
			}
			deliveryResults++
		case CommandResultOrderPlaced:
			if record.CommandType != "ConfirmOrder" || record.Result.Order == nil {
				return ErrInvalidSnapshot
			}
			order, exists := state.Orders[record.Result.Order.OrderID]
			if !exists || !reflect.DeepEqual(order, *record.Result.Order) {
				return ErrInvalidSnapshot
			}
			orderResults++
		case CommandResultBatchExpired:
			if record.CommandType != "ExpireBatch" || record.Result.Expiry == nil {
				return ErrInvalidSnapshot
			}
			expiry := record.Result.Expiry
			batch, exists := state.Batches[expiry.BatchID]
			if !exists || expiry.ProductID != batch.ProductID || expiry.Reason != ExpiryReasonExpiration || expiry.WrittenOffQuantity <= 0 || !expiry.ExpiresAt.Equal(batch.ExpiresAt) {
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
	if productResults != len(state.Products) || dishResults != len(state.Dishes) || deliveryResults != len(state.Deliveries) || orderResults != len(state.Orders) {
		return ErrInvalidSnapshot
	}
	for id, batch := range state.Batches {
		if expired[id] != batch.WrittenOffForExpiry {
			return fmt.Errorf("%w: batch %q write-off does not match command results", ErrInvalidSnapshot, id)
		}
	}
	return nil
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
