package application

import (
	"context"
	"sort"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func (o *Owner) PrepareStock(ctx context.Context) (contract.StockPreparation, error) {
	if err := o.mu.Lock(ctx); err != nil {
		return contract.StockPreparation{Products: []contract.Product{}, Batches: []contract.Batch{}}, inventoryUnavailable(err)
	}
	defer o.mu.Unlock()
	prepared, transitions, err := o.prepareLocked(ctx)
	result := contract.StockPreparation{EvaluatedAt: prepared.EvaluatedAt, StateVersion: o.state.StateVersion, InvalidationRequired: len(transitions) > 0, Products: make([]contract.Product, 0), Batches: make([]contract.Batch, 0)}
	if err != nil {
		return result, inventoryUnavailable(err)
	}
	result.StateVersion = prepared.StateVersion
	for _, p := range prepared.State.Products {
		result.Products = append(result.Products, toContractProduct(p))
	}
	sort.Slice(result.Products, func(i, j int) bool { return result.Products[i].ProductID < result.Products[j].ProductID })
	for _, b := range prepared.State.Batches {
		if b.RemainingQuantity > 0 {
			result.Batches = append(result.Batches, toContractBatch(b))
		}
	}
	sort.Slice(result.Batches, func(i, j int) bool {
		a, b := result.Batches[i], result.Batches[j]
		if !a.ExpiresAt.Equal(b.ExpiresAt) {
			return a.ExpiresAt.Before(b.ExpiresAt)
		}
		if !a.ReceivedAt.Equal(b.ReceivedAt) {
			return a.ReceivedAt.Before(b.ReceivedAt)
		}
		return a.BatchID < b.BatchID
	})
	return result, nil
}
func CalculateStock(ctx context.Context, prepared contract.StockPreparation) (contract.InventoryView, error) {
	if err := ctx.Err(); err != nil {
		return contract.InventoryView{}, err
	}
	view := contract.InventoryView{EvaluatedAt: prepared.EvaluatedAt, StateVersion: prepared.StateVersion, Products: make([]contract.ProductInventory, 0, len(prepared.Products))}
	for _, p := range prepared.Products {
		row := contract.ProductInventory{ProductID: p.ProductID, Name: p.Name, BaseUnit: p.BaseUnit, Batches: make([]contract.Batch, 0)}
		for _, b := range prepared.Batches {
			if b.ProductID != p.ProductID {
				continue
			}
			q, err := domain.AddQuantity(domain.Quantity(row.Quantity), domain.Quantity(b.RemainingQuantity))
			if err != nil {
				return contract.InventoryView{}, inventoryUnavailable(err)
			}
			row.Quantity = contract.Quantity(q)
			if b.PendingWriteoff {
				row.PendingWriteoffQuantity += b.RemainingQuantity
			} else {
				row.AvailableQuantity += b.RemainingQuantity
				if !b.ExpiresAt.After(prepared.EvaluatedAt.Add(48 * time.Hour)) {
					row.ExpiringQuantity += b.RemainingQuantity
				}
			}
			row.Batches = append(row.Batches, b)
		}
		view.Products = append(view.Products, row)
	}
	return view, nil
}
