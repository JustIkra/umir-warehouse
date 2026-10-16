package domain

import (
	"errors"
	"math"
)

type Quantity int64

var ErrQuantityOverflow = errors.New("quantity overflow")
var ErrInvalidQuantity = errors.New("invalid quantity")

func AddQuantity(a, b Quantity) (Quantity, error) {
	left, right := int64(a), int64(b)
	if right > 0 && left > math.MaxInt64-right || right < 0 && left < math.MinInt64-right {
		return 0, ErrQuantityOverflow
	}
	return Quantity(left + right), nil
}

// ValidateInventoryTotalsWith checks the per-product stock totals of state with
// the batches of delta substituted for the ones they replace, without copying
// the state. Only the products the delta touches can change, and an indexed
// state lists the batches that hold their stock, so the check reads those.
func ValidateInventoryTotalsWith(state *InventoryState, delta StateDelta) error {
	totals := make(map[string]Quantity)
	add := func(batch Batch) error {
		if batch.RemainingQuantity < 0 {
			return ErrInvalidQuantity
		}
		total, err := AddQuantity(totals[batch.ProductID], batch.RemainingQuantity)
		totals[batch.ProductID] = total
		return err
	}
	if state.Indexed() {
		touched := make(map[string]struct{})
		for _, batch := range delta.Batches {
			touched[batch.ProductID] = struct{}{}
		}
		for productID := range touched {
			for _, id := range state.productBatchIDs(productID) {
				if _, replaced := delta.Batches[id]; replaced {
					continue
				}
				if err := add(state.Batches[id]); err != nil {
					return err
				}
			}
		}
	} else {
		for id, batch := range state.Batches {
			if _, replaced := delta.Batches[id]; replaced {
				continue
			}
			if err := add(batch); err != nil {
				return err
			}
		}
	}
	for _, batch := range delta.Batches {
		if err := add(batch); err != nil {
			return err
		}
	}
	return nil
}

func MultiplyQuantity(quantity Quantity, portions int64) (Quantity, error) {
	if quantity <= 0 || portions <= 0 {
		return 0, ErrInvalidQuantity
	}
	if quantity != 0 && portions > math.MaxInt64/int64(quantity) {
		return 0, ErrQuantityOverflow
	}
	return Quantity(int64(quantity) * portions), nil
}
