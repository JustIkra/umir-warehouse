package domain

import (
	"math/big"
	"sort"
	"time"
)

const (
	SupplyWindowDays    = 7
	SupplyThresholdDays = 3
	SupplyTargetDays    = 7
)

type SupplySuggestion struct {
	Product                                                                   Product
	AvailableQuantity, ConsumedQuantity, OnePortionDeficit, SuggestedQuantity Quantity
	BlockedDishIDs                                                            []string
	LowCoverage                                                               bool
}

func EligibleBatch(batch Batch, at time.Time) bool {
	return batch.RemainingQuantity > 0 && !batch.PendingWriteoff && at.Before(batch.ExpiresAt)
}

func CalculateSupply(state InventoryState, consumed map[string]Quantity, at time.Time) ([]SupplySuggestion, error) {
	available := make(map[string]Quantity)
	for _, batch := range state.Batches {
		if !EligibleBatch(batch, at) {
			continue
		}
		if _, ok := state.Products[batch.ProductID]; !ok {
			return nil, ErrProductNotFound
		}
		value, err := AddQuantity(available[batch.ProductID], batch.RemainingQuantity)
		if err != nil {
			return nil, err
		}
		available[batch.ProductID] = value
	}
	deficits := make(map[string]Quantity)
	blocked := make(map[string][]string)
	for _, dish := range state.Dishes {
		for _, ingredient := range dish.Ingredients {
			if ingredient.Quantity <= 0 {
				return nil, ErrInvalidQuantity
			}
			if _, ok := state.Products[ingredient.ProductID]; !ok {
				return nil, ErrProductNotFound
			}
			deficit := ingredient.Quantity - available[ingredient.ProductID]
			if deficit > 0 {
				deficits[ingredient.ProductID] = max(deficits[ingredient.ProductID], deficit)
				blocked[ingredient.ProductID] = append(blocked[ingredient.ProductID], dish.DishID)
			}
		}
	}
	for id, quantity := range consumed {
		if quantity < 0 {
			return nil, ErrInvalidQuantity
		}
		if _, ok := state.Products[id]; !ok {
			return nil, ErrProductNotFound
		}
	}
	rows := make([]SupplySuggestion, 0)
	for id, product := range state.Products {
		usage := consumed[id]
		stock := available[id]
		left := new(big.Int).Mul(big.NewInt(int64(stock)), big.NewInt(SupplyWindowDays))
		right := new(big.Int).Mul(big.NewInt(int64(usage)), big.NewInt(SupplyThresholdDays))
		low := usage > 0 && left.Cmp(right) < 0
		if !low && deficits[id] == 0 {
			continue
		}
		target := new(big.Int).Mul(big.NewInt(int64(usage)), big.NewInt(SupplyTargetDays))
		target.Add(target, big.NewInt(SupplyWindowDays-1))
		target.Quo(target, big.NewInt(SupplyWindowDays))
		target.Sub(target, big.NewInt(int64(stock)))
		if !target.IsInt64() {
			return nil, ErrQuantityOverflow
		}
		quantity := max(Quantity(target.Int64()), deficits[id], 0)
		ids := blocked[id]
		if ids == nil {
			ids = []string{}
		}
		sort.Strings(ids)
		rows = append(rows, SupplySuggestion{Product: product, AvailableQuantity: stock, ConsumedQuantity: usage, OnePortionDeficit: deficits[id], SuggestedQuantity: quantity, BlockedDishIDs: ids, LowCoverage: low})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Product.ProductID < rows[j].Product.ProductID })
	return rows, nil
}
