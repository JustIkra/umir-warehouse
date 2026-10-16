package domain

import "slices"

// productBatchIDs returns the IDs of the batches that can supply one product:
// those with stock that are not awaiting a write-off. A state with a current
// index answers from it; any other is searched in full.
func (s InventoryState) productBatchIDs(productID string) []string {
	if s.indexed == len(s.Batches) {
		return s.byProduct[productID]
	}
	var ids []string
	for id, batch := range s.Batches {
		if batch.ProductID == productID && canSupply(batch) {
			ids = append(ids, id)
		}
	}
	return ids
}

// canSupply reports whether a batch can be drawn from by an order.
func canSupply(batch Batch) bool {
	return batch.RemainingQuantity > 0 && !batch.PendingWriteoff
}

// reindex rebuilds the index of a state from its batches.
func (s *InventoryState) reindex() {
	s.byProduct = nil
	for id, batch := range s.Batches {
		if canSupply(batch) {
			s.addToIndex(id, batch.ProductID)
		}
	}
	s.indexed = len(s.Batches)
}

// cloneIndex gives a cloned state its own index: a copy of the current one,
// or a new one when the source state has none.
func (s *InventoryState) cloneIndex(source InventoryState) {
	if source.indexed != len(source.Batches) {
		s.reindex()
		return
	}
	s.byProduct = nil
	if source.byProduct != nil {
		s.byProduct = make(map[string][]string, len(source.byProduct))
	}
	for productID, ids := range source.byProduct {
		s.byProduct[productID] = slices.Clone(ids)
	}
	s.indexed = source.indexed
}

// indexBatches brings the index up to date with the batches of delta, whether
// they are new or replace a recorded batch. It must run before the delta is
// copied into the state, and only extends an index that is current.
func (s *InventoryState) indexBatches(delta map[string]Batch) {
	if s.indexed != len(s.Batches) {
		return
	}
	for id, batch := range delta {
		recorded, exists := s.Batches[id]
		if !exists {
			s.indexed++
		}
		switch supplies := canSupply(batch); {
		case supplies && (!exists || !canSupply(recorded)):
			s.addToIndex(id, batch.ProductID)
		case !supplies && exists && canSupply(recorded):
			s.removeFromIndex(id, recorded.ProductID)
		}
	}
}

func (s *InventoryState) removeFromIndex(batchID, productID string) {
	ids := slices.DeleteFunc(s.byProduct[productID], func(id string) bool { return id == batchID })
	if len(ids) == 0 {
		delete(s.byProduct, productID)
		return
	}
	s.byProduct[productID] = ids
}

func (s *InventoryState) addToIndex(batchID, productID string) {
	if s.byProduct == nil {
		s.byProduct = make(map[string][]string)
	}
	s.byProduct[productID] = append(s.byProduct[productID], batchID)
}
