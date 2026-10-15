package domain

import "maps"

func CloneDish(value Dish) Dish {
	value.Ingredients = append([]Ingredient(nil), value.Ingredients...)
	return value
}

func CloneDelivery(value Delivery) Delivery {
	value.Lines = append([]DeliveryLine(nil), value.Lines...)
	return value
}

func CloneOrder(value Order) Order {
	value.AcceptedPositions = append([]AcceptedPosition(nil), value.AcceptedPositions...)
	value.Allocations = append([]Allocation(nil), value.Allocations...)
	return value
}

func CloneExpiryResult(value ExpiryResult) ExpiryResult {
	return value
}

func CloneEvent(value Event) Event {
	if value.Payload.Product != nil {
		product := *value.Payload.Product
		value.Payload.Product = &product
	}
	if value.Payload.Dish != nil {
		dish := CloneDish(*value.Payload.Dish)
		value.Payload.Dish = &dish
	}
	if value.Payload.Delivery != nil {
		delivery := CloneDelivery(*value.Payload.Delivery)
		value.Payload.Delivery = &delivery
	}
	value.Payload.Batches = append([]Batch(nil), value.Payload.Batches...)
	if value.Payload.Order != nil {
		order := CloneOrder(*value.Payload.Order)
		value.Payload.Order = &order
	}
	if value.Payload.Pending != nil {
		v := *value.Payload.Pending
		value.Payload.Pending = &v
	}
	if value.Payload.Writeoff != nil {
		v := *value.Payload.Writeoff
		value.Payload.Writeoff = &v
	}
	if value.Payload.Expiry != nil {
		expiry := CloneExpiryResult(*value.Payload.Expiry)
		value.Payload.Expiry = &expiry
	}
	return value
}

func CloneCommandResult(value CommandResult) CommandResult {
	if value.Product != nil {
		product := *value.Product
		value.Product = &product
	}
	if value.Dish != nil {
		dish := CloneDish(*value.Dish)
		value.Dish = &dish
	}
	if value.Delivery != nil {
		receipt := *value.Delivery
		receipt.BatchIDs = append([]string(nil), receipt.BatchIDs...)
		receipt.Batches = append([]Batch(nil), receipt.Batches...)
		value.Delivery = &receipt
	}
	if value.Order != nil {
		order := CloneOrder(*value.Order)
		value.Order = &order
	}
	if value.Pending != nil {
		v := *value.Pending
		value.Pending = &v
	}
	if value.Writeoff != nil {
		v := *value.Writeoff
		value.Writeoff = &v
	}
	if value.Expiry != nil {
		expiry := CloneExpiryResult(*value.Expiry)
		value.Expiry = &expiry
	}
	return value
}

func CloneCommandRecord(value CommandRecord) CommandRecord {
	value.Result = CloneCommandResult(value.Result)
	return value
}

func CloneRecordedBatch(value RecordedBatch) RecordedBatch {
	events := value.Events
	value.Events = make([]Event, len(events))
	for i, event := range events {
		value.Events[i] = CloneEvent(event)
	}
	return value
}

func CloneInventoryState(value InventoryState) InventoryState {
	value.Products = maps.Clone(value.Products)
	value.Dishes = cloneDishes(value.Dishes)
	value.DeliveryIDs = maps.Clone(value.DeliveryIDs)
	value.Batches = maps.Clone(value.Batches)
	return value
}

// CloneReadView copies what a reader of the current inventory needs:
// products, dishes and batches. DeliveryIDs is left out and nil.
func CloneReadView(value InventoryState) InventoryState {
	return InventoryState{
		Products:     maps.Clone(value.Products),
		Dishes:       cloneDishes(value.Dishes),
		Batches:      maps.Clone(value.Batches),
		StateVersion: value.StateVersion,
	}
}

func cloneDishes(dishes map[string]Dish) map[string]Dish {
	if dishes == nil {
		return nil
	}
	cloned := make(map[string]Dish, len(dishes))
	for id, dish := range dishes {
		cloned[id] = CloneDish(dish)
	}
	return cloned
}

func CloneSnapshot(value Snapshot) Snapshot {
	value.State = CloneInventoryState(value.State)
	return value
}
