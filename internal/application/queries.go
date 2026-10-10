package application

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func (o *Owner) GetInventory(ctx context.Context) (contract.InventoryView, error) {
	prepared, err := o.Prepare(ctx)
	if err != nil {
		return contract.InventoryView{}, err
	}
	productIDs := make([]string, 0, len(prepared.State.Products))
	for productID := range prepared.State.Products {
		productIDs = append(productIDs, productID)
	}
	sort.Strings(productIDs)
	view := contract.InventoryView{EvaluatedAt: prepared.EvaluatedAt, StateVersion: prepared.StateVersion, Products: make([]contract.ProductInventory, 0, len(productIDs))}
	for _, productID := range productIDs {
		product := prepared.State.Products[productID]
		batches := make([]domain.Batch, 0)
		for _, batch := range prepared.State.Batches {
			if batch.ProductID == productID && batch.RemainingQuantity > 0 {
				batches = append(batches, batch)
			}
		}
		sort.Slice(batches, func(i, j int) bool {
			if !batches[i].ExpiresAt.Equal(batches[j].ExpiresAt) {
				return batches[i].ExpiresAt.Before(batches[j].ExpiresAt)
			}
			if !batches[i].ReceivedAt.Equal(batches[j].ReceivedAt) {
				return batches[i].ReceivedAt.Before(batches[j].ReceivedAt)
			}
			return batches[i].BatchID < batches[j].BatchID
		})
		item := contract.ProductInventory{ProductID: productID, BaseUnit: contract.BaseUnit(product.BaseUnit), Batches: make([]contract.Batch, 0, len(batches))}
		for _, batch := range batches {
			total, err := domain.AddQuantity(domain.Quantity(item.Quantity), batch.RemainingQuantity)
			if err != nil {
				return contract.InventoryView{}, inventoryUnavailable(err)
			}
			item.Quantity = contract.Quantity(total)
			item.Batches = append(item.Batches, toContractBatch(batch))
		}
		view.Products = append(view.Products, item)
	}
	return view, nil
}

func (o *Owner) GetExpiringBatches(ctx context.Context, cutoffAt time.Time) (contract.ExpiringBatches, error) {
	if !businessTime(cutoffAt) {
		return contract.ExpiringBatches{}, contract.ErrInvalidArgument
	}
	prepared, err := o.Prepare(ctx)
	if err != nil {
		return contract.ExpiringBatches{}, err
	}
	if cutoffAt.Before(prepared.EvaluatedAt) {
		return contract.ExpiringBatches{}, contract.ErrInvalidArgument
	}
	items := make([]contract.ExpiringBatch, 0)
	for _, batch := range prepared.State.Batches {
		if batch.RemainingQuantity <= 0 || batch.ExpiresAt.After(cutoffAt) {
			continue
		}
		product := prepared.State.Products[batch.ProductID]
		items = append(items, contract.ExpiringBatch{
			BatchID:           batch.BatchID,
			ProductID:         batch.ProductID,
			RemainingQuantity: contract.Quantity(batch.RemainingQuantity),
			BaseUnit:          contract.BaseUnit(product.BaseUnit),
			ExpiresAt:         batch.ExpiresAt,
			RemainingSeconds:  remainingSeconds(batch.ExpiresAt, prepared.EvaluatedAt),
			IsExpired:         false,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].ExpiresAt.Equal(items[j].ExpiresAt) {
			return items[i].ExpiresAt.Before(items[j].ExpiresAt)
		}
		return items[i].BatchID < items[j].BatchID
	})
	return contract.ExpiringBatches{EvaluatedAt: prepared.EvaluatedAt, StateVersion: prepared.StateVersion, Items: items}, nil
}

func (o *Owner) GetHistory(ctx context.Context) (contract.History, error) {
	batches, err := o.events.ReadBatches(ctx, 0)
	if err != nil {
		return contract.History{}, err
	}
	items := make([]contract.Event, 0)
	var stateVersion int64
	for _, batch := range batches {
		for _, event := range batch.Events {
			items = append(items, toContractEvent(event))
			stateVersion = event.Sequence
		}
	}
	return contract.History{EvaluatedAt: domain.CanonicalBusinessTime(o.now()), StateVersion: stateVersion, Events: items}, nil
}

func remainingSeconds(expiresAt, evaluatedAt time.Time) int64 {
	if !expiresAt.After(evaluatedAt) {
		return 0
	}
	seconds := expiresAt.Unix() - evaluatedAt.Unix()
	if expiresAt.Nanosecond() < evaluatedAt.Nanosecond() {
		seconds--
	}
	if seconds < 0 {
		return 0
	}
	if seconds == math.MaxInt64 {
		return math.MaxInt64
	}
	return seconds
}

func toContractBatch(value domain.Batch) contract.Batch {
	return contract.Batch{
		BatchID:           value.BatchID,
		DeliveryID:        value.DeliveryID,
		LineNo:            value.LineNo,
		ProductID:         value.ProductID,
		ManufacturedAt:    value.ManufacturedAt,
		ShelfLife:         contract.ShelfLife{Value: value.ShelfLife.Value, Unit: contract.ShelfLifeUnit(value.ShelfLife.Unit)},
		ReceivedAt:        value.ReceivedAt,
		ExpiresAt:         value.ExpiresAt,
		ReceivedQuantity:  contract.Quantity(value.ReceivedQuantity),
		RemainingQuantity: contract.Quantity(value.RemainingQuantity),
	}
}

func toContractEvent(value domain.Event) contract.Event {
	result := contract.Event{
		EventID:            value.EventID,
		StreamID:           value.StreamID,
		Sequence:           value.Sequence,
		SchemaVersion:      value.SchemaVersion,
		CommandID:          value.CommandID,
		CommandFingerprint: value.CommandFingerprint,
		RecordedAt:         value.RecordedAt,
		Type:               contract.EventType(value.Type),
	}
	if value.Payload.Product != nil {
		product := toContractProduct(*value.Payload.Product)
		result.Payload.Product = &product
	}
	if value.Payload.Dish != nil {
		dish := contract.Dish{DishID: value.Payload.Dish.DishID, Name: value.Payload.Dish.Name, Ingredients: make([]contract.Ingredient, len(value.Payload.Dish.Ingredients))}
		for index, ingredient := range value.Payload.Dish.Ingredients {
			product := value.Payload.Dish.Ingredients[index].ProductID
			dish.Ingredients[index] = contract.Ingredient{ProductID: product, Quantity: contract.Quantity(ingredient.Quantity)}
		}
		result.Payload.Dish = &dish
	}
	if value.Payload.Delivery != nil {
		delivery := contract.Delivery{DeliveryID: value.Payload.Delivery.DeliveryID, Number: value.Payload.Delivery.Number, CreatedAt: value.Payload.Delivery.CreatedAt, Lines: make([]contract.DeliveryLine, len(value.Payload.Delivery.Lines))}
		for index, line := range value.Payload.Delivery.Lines {
			delivery.Lines[index] = contract.DeliveryLine{ProductID: line.ProductID, ManufacturedAt: line.ManufacturedAt, ShelfLife: contract.ShelfLife{Value: line.ShelfLife.Value, Unit: contract.ShelfLifeUnit(line.ShelfLife.Unit)}, ReceivedAt: line.ReceivedAt, Quantity: contract.Quantity(line.Quantity)}
		}
		result.Payload.Delivery = &delivery
	}
	if value.Payload.Batches != nil {
		result.Payload.Batches = make([]contract.Batch, len(value.Payload.Batches))
		for index, batch := range value.Payload.Batches {
			result.Payload.Batches[index] = toContractBatch(batch)
		}
	}
	if value.Payload.Order != nil {
		order := toContractOrder(*value.Payload.Order)
		result.Payload.Order = &order
	}
	if value.Payload.Expiry != nil {
		expiry := contract.ExpiryResult{BatchID: value.Payload.Expiry.BatchID, ProductID: value.Payload.Expiry.ProductID, WrittenOffQuantity: contract.Quantity(value.Payload.Expiry.WrittenOffQuantity), Reason: value.Payload.Expiry.Reason, ExpiresAt: value.Payload.Expiry.ExpiresAt, ProcessedAt: value.Payload.Expiry.ProcessedAt}
		result.Payload.Expiry = &expiry
	}
	return result
}
