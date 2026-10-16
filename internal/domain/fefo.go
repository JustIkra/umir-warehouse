package domain

import (
	"errors"
	"sort"
)

var ErrInsufficientQuantity = errors.New("insufficient quantity")

func AllocateFEFO(batches []Batch, required Quantity) ([]Allocation, error) {
	if required < 0 {
		return nil, ErrInvalidQuantity
	}
	if required == 0 {
		return []Allocation{}, nil
	}
	ordered := make([]Batch, len(batches))
	copy(ordered, batches)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].ExpiresAt.Equal(ordered[j].ExpiresAt) {
			return ordered[i].ExpiresAt.Before(ordered[j].ExpiresAt)
		}
		if !ordered[i].ReceivedAt.Equal(ordered[j].ReceivedAt) {
			return ordered[i].ReceivedAt.Before(ordered[j].ReceivedAt)
		}
		return ordered[i].BatchID < ordered[j].BatchID
	})

	remaining := required
	allocations := make([]Allocation, 0, len(ordered))
	for _, batch := range ordered {
		if batch.RemainingQuantity <= 0 || batch.PendingWriteoff {
			continue
		}
		allocated := batch.RemainingQuantity
		if allocated > remaining {
			allocated = remaining
		}
		allocations = append(allocations, Allocation{
			ProductID: batch.ProductID,
			BatchID:   batch.BatchID,
			Quantity:  allocated,
		})
		remaining -= allocated
		if remaining == 0 {
			return allocations, nil
		}
	}
	return nil, ErrInsufficientQuantity
}

func pureEvaluatePositions(state InventoryState, positions []Position) (OrderEvaluation, error) {
	evaluation, _, err := evaluatePositions(state, positions)
	return evaluation, err
}

func PureEvaluatePositions(state InventoryState, positions []Position) (OrderEvaluation, error) {
	return pureEvaluatePositions(state, positions)
}

func EvaluatePositionsWithAllocations(state InventoryState, positions []Position) (OrderEvaluation, []Allocation, error) {
	return evaluatePositions(state, positions)
}

func evaluatePositions(state InventoryState, positions []Position) (OrderEvaluation, []Allocation, error) {
	evaluation := OrderEvaluation{
		StateVersion:       state.StateVersion,
		RequestedPositions: append([]Position(nil), positions...),
		AcceptedPositions:  make([]AcceptedPosition, 0, len(positions)),
		Changes:            make([]Change, 0),
	}
	// working holds the remaining quantity of the batches this order has
	// already drawn from; every other batch still has its recorded quantity.
	working := make(map[string]Quantity)
	allocations := make([]Allocation, 0)
	seenLines := make(map[int64]struct{}, len(positions))
	for _, position := range positions {
		if position.LineNo <= 0 || position.DishID == "" || position.Portions <= 0 {
			return OrderEvaluation{}, nil, ErrInvalidQuantity
		}
		if _, exists := seenLines[position.LineNo]; exists {
			return OrderEvaluation{}, nil, ErrInvalidQuantity
		}
		seenLines[position.LineNo] = struct{}{}
		dish, exists := state.Dishes[position.DishID]
		if !exists {
			return OrderEvaluation{}, nil, ErrDishNotFound
		}
		maximum := position.Portions
		availableByProduct := make(map[string]Quantity, len(dish.Ingredients))
		if len(dish.Ingredients) == 0 {
			return OrderEvaluation{}, nil, ErrInvalidQuantity
		}
		for _, ingredient := range dish.Ingredients {
			if ingredient.Quantity <= 0 {
				return OrderEvaluation{}, nil, ErrInvalidQuantity
			}
			available, err := availableQuantity(state, working, ingredient.ProductID)
			if err != nil {
				return OrderEvaluation{}, nil, err
			}
			availableByProduct[ingredient.ProductID] = available
			possible := int64(available / ingredient.Quantity)
			if possible < maximum {
				maximum = possible
			}
		}
		if maximum < 0 {
			maximum = 0
		}
		if maximum > 0 {
			accepted := AcceptedPosition{LineNo: position.LineNo, DishID: position.DishID, Portions: maximum}
			evaluation.AcceptedPositions = append(evaluation.AcceptedPositions, accepted)
			for _, ingredient := range dish.Ingredients {
				required, err := MultiplyQuantity(ingredient.Quantity, maximum)
				if err != nil {
					return OrderEvaluation{}, nil, err
				}
				batches := productBatches(state, working, ingredient.ProductID)
				allocated, err := AllocateFEFO(batches, required)
				if err != nil {
					return OrderEvaluation{}, nil, err
				}
				for _, allocation := range allocated {
					working[allocation.BatchID] = workingQuantity(working, allocation.BatchID, state.Batches[allocation.BatchID]) - allocation.Quantity
				}
				allocations = append(allocations, allocated...)
			}
		}
		if maximum < position.Portions {
			change := Change{LineNo: position.LineNo, DishID: position.DishID, RequestedPortions: position.Portions, AcceptedPortions: maximum, Deficits: make([]Deficit, 0, len(dish.Ingredients))}
			for _, ingredient := range dish.Ingredients {
				required, err := MultiplyQuantity(ingredient.Quantity, position.Portions)
				if err != nil {
					return OrderEvaluation{}, nil, err
				}
				available := availableByProduct[ingredient.ProductID]
				if available < required {
					change.Deficits = append(change.Deficits, Deficit{ProductID: ingredient.ProductID, RequiredQuantity: required, AvailableQuantity: available})
				}
			}
			evaluation.Changes = append(evaluation.Changes, change)
		}
	}
	return evaluation, allocations, nil
}

func workingQuantity(working map[string]Quantity, id string, batch Batch) Quantity {
	if batch.PendingWriteoff {
		return 0
	}
	if quantity, drawn := working[id]; drawn {
		return quantity
	}
	return batch.RemainingQuantity
}

func availableQuantity(state InventoryState, working map[string]Quantity, productID string) (Quantity, error) {
	var available Quantity
	for _, batchID := range state.productBatchIDs(productID) {
		quantity := workingQuantity(working, batchID, state.Batches[batchID])
		if quantity <= 0 {
			continue
		}
		var err error
		available, err = AddQuantity(available, quantity)
		if err != nil {
			return 0, err
		}
	}
	return available, nil
}

func productBatches(state InventoryState, working map[string]Quantity, productID string) []Batch {
	ids := state.productBatchIDs(productID)
	batches := make([]Batch, 0, len(ids))
	for _, batchID := range ids {
		batch := state.Batches[batchID]
		quantity := workingQuantity(working, batchID, batch)
		if quantity <= 0 {
			continue
		}
		batch.RemainingQuantity = quantity
		batches = append(batches, batch)
	}
	return batches
}
