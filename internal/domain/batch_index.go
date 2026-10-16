package domain

import "slices"

// productBatchIDs returns the IDs of the batches of one product. A state with
// a current index answers from it; any other is searched in full.
func (s InventoryState) productBatchIDs(productID string) []string {
	if s.indexed == len(s.Batches) {
		return s.byProduct[productID]
	}
	var ids []string
	for id, batch := range s.Batches {
		if batch.ProductID == productID {
			ids = append(ids, id)
		}
	}
	return ids
}

// reindex rebuilds the index of a state from its batches.
func (s *InventoryState) reindex() {
	s.byProduct = nil
	for id, batch := range s.Batches {
		s.addToIndex(id, batch.ProductID)
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

// indexNewBatches lists the batches of delta that the state does not hold yet.
// It must run before the delta is copied into the state, and only extends an
// index that is current.
func (s *InventoryState) indexNewBatches(delta map[string]Batch) {
	if s.indexed != len(s.Batches) {
		return
	}
	for id, batch := range delta {
		if _, exists := s.Batches[id]; !exists {
			s.addToIndex(id, batch.ProductID)
			s.indexed++
		}
	}
}

func (s *InventoryState) addToIndex(batchID, productID string) {
	if s.byProduct == nil {
		s.byProduct = make(map[string][]string)
	}
	s.byProduct[productID] = append(s.byProduct[productID], batchID)
}
