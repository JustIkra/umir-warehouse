package domain

import (
	"errors"
	"fmt"
	"slices"
)

// SnapshotSchemaV2 is the snapshot layout without order and command history.
// It is separate from InventorySchemaV2, which versions events.
const SnapshotSchemaV2 = int64(2)

var ErrInvalidSnapshot = errors.New("invalid snapshot")

func ValidateSnapshot(snapshot Snapshot) error {
	if snapshot.SchemaVersion != SnapshotSchemaV2 {
		return ErrUnsupportedSchema
	}
	if snapshot.StreamID != InventoryStreamID || snapshot.LastSequence < 0 || snapshot.State.StateVersion != snapshot.LastSequence {
		return ErrInvalidSnapshot
	}
	return ValidateInventoryState(snapshot.State)
}

// EqualInventoryState reports whether two states hold the same records.
func EqualInventoryState(a, b InventoryState) bool {
	if a.StateVersion != b.StateVersion || len(a.Products) != len(b.Products) || len(a.Dishes) != len(b.Dishes) || len(a.DeliveryIDs) != len(b.DeliveryIDs) || len(a.Batches) != len(b.Batches) {
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
	for id := range a.DeliveryIDs {
		if _, exists := b.DeliveryIDs[id]; !exists {
			return false
		}
	}
	for id, value := range a.Batches {
		other, exists := b.Batches[id]
		if !exists || !equalBatch(value, other) {
			return false
		}
	}
	return true
}

func equalBatch(a, b Batch) bool {
	return a.BatchID == b.BatchID && a.DeliveryID == b.DeliveryID && a.LineNo == b.LineNo && a.ProductID == b.ProductID && a.ManufacturedAt.Equal(b.ManufacturedAt) && a.ShelfLife == b.ShelfLife && a.ReceivedAt.Equal(b.ReceivedAt) && a.ExpiresAt.Equal(b.ExpiresAt) && a.ReceivedQuantity == b.ReceivedQuantity && a.RemainingQuantity == b.RemainingQuantity && a.ConsumedByOrders == b.ConsumedByOrders && a.WrittenOffForExpiry == b.WrittenOffForExpiry && a.PendingWriteoff == b.PendingWriteoff && a.AppliedTimeZone == b.AppliedTimeZone && a.AppliedSettingsVersion == b.AppliedSettingsVersion && a.TZDataVersion == b.TZDataVersion
}

// ValidateInventoryState checks the structure of a state on its own: records
// are keyed by their IDs, references resolve and batch balances add up.
func ValidateInventoryState(state InventoryState) error {
	records := int64(len(state.Products)) + int64(len(state.Dishes)) + int64(len(state.DeliveryIDs))
	// Every product, dish and delivery comes from at least one event, so the
	// version cannot be below their count, and a state at a version is never empty.
	if state.StateVersion < 0 || state.StateVersion == 0 && (records > 0 || len(state.Batches) > 0) || state.StateVersion > 0 && (records == 0 || records > state.StateVersion) {
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
	return validateDeliveryIDsAndBatches(state)
}

func validateDeliveryIDsAndBatches(state InventoryState) error {
	for id := range state.DeliveryIDs {
		if id == "" {
			return ErrInvalidSnapshot
		}
	}
	lines := make(map[string]int, len(state.DeliveryIDs))
	lineSeen := make(map[string]map[int64]struct{}, len(state.DeliveryIDs))
	for id, batch := range state.Batches {
		if id == "" || id != batch.BatchID || batch.DeliveryID == "" || batch.ProductID == "" || batch.LineNo <= 0 || batch.ReceivedQuantity <= 0 || batch.ShelfLife.Value <= 0 || batch.ShelfLife.Unit != ShelfLifeDay && batch.ShelfLife.Unit != ShelfLifeMonth || !isBusinessTime(batch.ManufacturedAt) || !isBusinessTime(batch.ReceivedAt) || !isBusinessTime(batch.ExpiresAt) || !batch.ExpiresAt.After(batch.ManufacturedAt) || batch.PendingWriteoff && (batch.RemainingQuantity <= 0 || batch.WrittenOffForExpiry > 0) || batch.WrittenOffForExpiry > 0 && batch.RemainingQuantity > 0 || !validBatchBalance(batch) {
			return ErrInvalidSnapshot
		}
		if _, exists := state.Products[batch.ProductID]; !exists {
			return fmt.Errorf("%w: batch %q references missing product %q", ErrInvalidSnapshot, id, batch.ProductID)
		}
		if _, exists := state.DeliveryIDs[batch.DeliveryID]; !exists {
			return fmt.Errorf("%w: batch %q references missing delivery %q", ErrInvalidSnapshot, id, batch.DeliveryID)
		}
		if lineSeen[batch.DeliveryID] == nil {
			lineSeen[batch.DeliveryID] = map[int64]struct{}{}
		}
		if _, exists := lineSeen[batch.DeliveryID][batch.LineNo]; exists {
			return fmt.Errorf("%w: delivery %q has two batches on line %d", ErrInvalidSnapshot, batch.DeliveryID, batch.LineNo)
		}
		lineSeen[batch.DeliveryID][batch.LineNo] = struct{}{}
		lines[batch.DeliveryID]++
	}
	// A delivery always creates at least one batch, one for each of its lines
	// numbered from 1, and batches are never removed.
	for id := range state.DeliveryIDs {
		count := lines[id]
		if count == 0 {
			return fmt.Errorf("%w: delivery %q has no batches", ErrInvalidSnapshot, id)
		}
		for line := int64(1); line <= int64(count); line++ {
			if _, exists := lineSeen[id][line]; !exists {
				return fmt.Errorf("%w: delivery %q has no batch on line %d", ErrInvalidSnapshot, id, line)
			}
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
