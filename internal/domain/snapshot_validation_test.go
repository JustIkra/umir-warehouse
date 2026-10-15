package domain

import (
	"errors"
	"testing"
	"time"
)

func validationSnapshot() Snapshot {
	product := Product{ProductID: "product-1", Name: "Tomato", BaseUnit: BaseUnitG}
	return Snapshot{
		SchemaVersion: SnapshotSchemaV2,
		StreamID:      InventoryStreamID,
		LastSequence:  3,
		State: InventoryState{
			Products:    map[string]Product{"product-1": product},
			Dishes:      map[string]Dish{"dish-1": {DishID: "dish-1", Name: "Soup", Ingredients: []Ingredient{{ProductID: "product-1", Quantity: 500}}}},
			DeliveryIDs: map[string]struct{}{"delivery-1": {}},
			Batches: map[string]Batch{"batch-1": {
				BatchID: "batch-1", DeliveryID: "delivery-1", LineNo: 1, ProductID: "product-1",
				ManufacturedAt: validationTime(), ShelfLife: ShelfLife{Value: 2, Unit: ShelfLifeDay}, ReceivedAt: validationTime(), ExpiresAt: validationTime().AddDate(0, 0, 2),
				ReceivedQuantity: 1000, RemainingQuantity: 700, ConsumedByOrders: 300,
			}},
			StateVersion: 3,
		},
	}
}

func TestValidateSnapshotAcceptsOnlyTheCurrentSchema(t *testing.T) {
	snapshot := validationSnapshot()
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("ValidateSnapshot(valid snapshot) error = %v", err)
	}
	for _, version := range []int64{0, InventorySchemaV1, 3} {
		snapshot.SchemaVersion = version
		if err := ValidateSnapshot(snapshot); !errors.Is(err, ErrUnsupportedSchema) {
			t.Errorf("ValidateSnapshot(schema %d) error = %v; want ErrUnsupportedSchema", version, err)
		}
	}
}

func TestValidateSnapshotChecksIdentityAndVersion(t *testing.T) {
	for name, edit := range map[string]func(*Snapshot){
		"foreign stream":            func(s *Snapshot) { s.StreamID = "other" },
		"negative sequence":         func(s *Snapshot) { s.LastSequence = -1; s.State.StateVersion = -1 },
		"state version differs":     func(s *Snapshot) { s.State.StateVersion = 2 },
		"content without a version": func(s *Snapshot) { s.LastSequence = 0; s.State.StateVersion = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := validationSnapshot()
			edit(&snapshot)
			if err := ValidateSnapshot(snapshot); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("ValidateSnapshot() error = %v; want ErrInvalidSnapshot", err)
			}
		})
	}
	empty := Snapshot{SchemaVersion: SnapshotSchemaV2, StreamID: InventoryStreamID}
	if err := ValidateSnapshot(empty); err != nil {
		t.Fatalf("ValidateSnapshot(empty snapshot at sequence 0) error = %v", err)
	}
}

func TestValidateInventoryStateRejectsBrokenStructure(t *testing.T) {
	editBatch := func(edit func(*Batch)) func(*InventoryState) {
		return func(s *InventoryState) {
			batch := s.Batches["batch-1"]
			edit(&batch)
			s.Batches["batch-1"] = batch
		}
	}
	for name, edit := range map[string]func(*InventoryState){
		"product key differs from its ID": func(s *InventoryState) {
			s.Products["product-1"] = Product{ProductID: "other", Name: "Tomato", BaseUnit: BaseUnitG}
		},
		"product without a name": func(s *InventoryState) {
			s.Products["product-1"] = Product{ProductID: "product-1", BaseUnit: BaseUnitG}
		},
		"product with an unknown unit": func(s *InventoryState) {
			s.Products["product-1"] = Product{ProductID: "product-1", Name: "Tomato", BaseUnit: "kg"}
		},
		"dish without ingredients": func(s *InventoryState) { s.Dishes["dish-1"] = Dish{DishID: "dish-1", Name: "Soup"} },
		"dish with a missing product": func(s *InventoryState) {
			s.Dishes["dish-1"] = Dish{DishID: "dish-1", Name: "Soup", Ingredients: []Ingredient{{ProductID: "missing", Quantity: 1}}}
		},
		"dish with a non-positive quantity": func(s *InventoryState) {
			s.Dishes["dish-1"] = Dish{DishID: "dish-1", Name: "Soup", Ingredients: []Ingredient{{ProductID: "product-1", Quantity: 0}}}
		},
		"dish repeating an ingredient": func(s *InventoryState) {
			s.Dishes["dish-1"] = Dish{DishID: "dish-1", Name: "Soup", Ingredients: []Ingredient{{ProductID: "product-1", Quantity: 1}, {ProductID: "product-1", Quantity: 2}}}
		},
		"empty delivery ID":              func(s *InventoryState) { s.DeliveryIDs[""] = struct{}{} },
		"delivery without batches":       func(s *InventoryState) { s.DeliveryIDs["delivery-2"] = struct{}{} },
		"batch from an unknown delivery": editBatch(func(b *Batch) { b.DeliveryID = "missing" }),
		"batch of an unknown product":    editBatch(func(b *Batch) { b.ProductID = "missing" }),
		"batch without a delivery":       editBatch(func(b *Batch) { b.DeliveryID = "" }),
		"negative remaining balance":     editBatch(func(b *Batch) { b.RemainingQuantity = -1; b.ConsumedByOrders = 1001 }),
		"balance that does not add up":   editBatch(func(b *Batch) { b.RemainingQuantity = 800 }),
		"stock left after write-off":     editBatch(func(b *Batch) { b.WrittenOffForExpiry = 100; b.RemainingQuantity = 600 }),
		"pending batch without stock":    editBatch(func(b *Batch) { b.PendingWriteoff = true; b.RemainingQuantity = 0; b.ConsumedByOrders = 1000 }),
		"batch key differs from its ID":  func(s *InventoryState) { s.Batches["other"] = s.Batches["batch-1"]; delete(s.Batches, "batch-1") },
		"expiry before manufacture":      editBatch(func(b *Batch) { b.ExpiresAt = b.ManufacturedAt }),
		"received time outside the zone": editBatch(func(b *Batch) { b.ReceivedAt = b.ReceivedAt.UTC() }),
		"unsupported shelf life unit":    editBatch(func(b *Batch) { b.ShelfLife.Unit = "year" }),
		"non-positive received quantity": editBatch(func(b *Batch) { b.ReceivedQuantity = 0; b.RemainingQuantity = 0; b.ConsumedByOrders = 0 }),
	} {
		t.Run(name, func(t *testing.T) {
			state := validationSnapshot().State
			edit(&state)
			if err := ValidateInventoryState(state); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("ValidateInventoryState() error = %v; want ErrInvalidSnapshot", err)
			}
		})
	}
}

func TestEqualInventoryState(t *testing.T) {
	base := validationSnapshot().State
	if !EqualInventoryState(base, CloneInventoryState(base)) {
		t.Fatal("a clone is not equal to its source")
	}
	otherZone := CloneInventoryState(base)
	batch := otherZone.Batches["batch-1"]
	batch.ExpiresAt = batch.ExpiresAt.In(time.UTC)
	otherZone.Batches["batch-1"] = batch
	if !EqualInventoryState(base, otherZone) {
		t.Fatal("equal instants in another zone are reported as different")
	}
	for name, edit := range map[string]func(*InventoryState){
		"version": func(s *InventoryState) { s.StateVersion++ },
		"product": func(s *InventoryState) {
			s.Products["product-1"] = Product{ProductID: "product-1", Name: "Potato", BaseUnit: BaseUnitG}
		},
		"extra dish":  func(s *InventoryState) { s.Dishes["dish-2"] = Dish{DishID: "dish-2"} },
		"dish recipe": func(s *InventoryState) { s.Dishes["dish-1"].Ingredients[0].Quantity++ },
		"delivery ID": func(s *InventoryState) { delete(s.DeliveryIDs, "delivery-1"); s.DeliveryIDs["delivery-2"] = struct{}{} },
		"batch":       func(s *InventoryState) { b := s.Batches["batch-1"]; b.PendingWriteoff = true; s.Batches["batch-1"] = b },
		"batch ID":    func(s *InventoryState) { s.Batches["batch-2"] = s.Batches["batch-1"]; delete(s.Batches, "batch-1") },
	} {
		t.Run(name, func(t *testing.T) {
			changed := CloneInventoryState(base)
			edit(&changed)
			if EqualInventoryState(base, changed) || EqualInventoryState(changed, base) {
				t.Fatal("a difference went unnoticed")
			}
		})
	}
}

func validationTime() time.Time {
	return time.Date(2026, time.March, 1, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
}
