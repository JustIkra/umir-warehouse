package domain

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
		value.Delivery = &receipt
	}
	if value.Order != nil {
		order := CloneOrder(*value.Order)
		value.Order = &order
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
	if value.Products != nil {
		products := make(map[string]Product, len(value.Products))
		for id, product := range value.Products {
			products[id] = product
		}
		value.Products = products
	}
	if value.Dishes != nil {
		dishes := make(map[string]Dish, len(value.Dishes))
		for id, dish := range value.Dishes {
			dishes[id] = CloneDish(dish)
		}
		value.Dishes = dishes
	}
	if value.Deliveries != nil {
		deliveries := make(map[string]Delivery, len(value.Deliveries))
		for id, delivery := range value.Deliveries {
			deliveries[id] = CloneDelivery(delivery)
		}
		value.Deliveries = deliveries
	}
	if value.Batches != nil {
		batches := make(map[string]Batch, len(value.Batches))
		for id, batch := range value.Batches {
			batches[id] = batch
		}
		value.Batches = batches
	}
	if value.Orders != nil {
		orders := make(map[string]Order, len(value.Orders))
		for id, order := range value.Orders {
			orders[id] = CloneOrder(order)
		}
		value.Orders = orders
	}
	if value.OrdersByProposal != nil {
		ordersByProposal := make(map[string]string, len(value.OrdersByProposal))
		for proposalID, orderID := range value.OrdersByProposal {
			ordersByProposal[proposalID] = orderID
		}
		value.OrdersByProposal = ordersByProposal
	}
	if value.Commands != nil {
		commands := make(map[string]CommandRecord, len(value.Commands))
		for id, command := range value.Commands {
			commands[id] = CloneCommandRecord(command)
		}
		value.Commands = commands
	}
	return value
}

func CloneSnapshot(value Snapshot) Snapshot {
	value.State = CloneInventoryState(value.State)
	return value
}
