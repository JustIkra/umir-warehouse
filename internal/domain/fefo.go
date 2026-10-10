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
		if batch.RemainingQuantity <= 0 {
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
