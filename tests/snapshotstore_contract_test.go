package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestSnapshotStoreCopiesAndPublishesMonotonically(t *testing.T) {
	store := memory.NewSnapshotStore()
	ctx := context.Background()
	candidate := validSnapshot(2)
	candidate.State.Products["product-1"] = domain.Product{ProductID: "product-1", Name: "Tomato", BaseUnit: domain.BaseUnitG}
	candidate.State.Dishes = map[string]domain.Dish{"dish-1": {DishID: "dish-1", Name: "Soup", Ingredients: []domain.Ingredient{{ProductID: "product-1", Quantity: 500}}}}
	candidate.State.Commands["command-1"] = productCommand("command-1", "product-1")
	candidate.State.Commands["command-2"] = dishCommand("command-2", "dish-1", "product-1")

	if err := store.Save(ctx, candidate); err != nil {
		t.Fatalf("Save(valid candidate) error = %v", err)
	}
	candidate.State.Dishes["dish-1"].Ingredients[0].Quantity = 900
	loaded, found, err := store.LoadLatest(ctx)
	if err != nil || !found {
		t.Fatalf("LoadLatest() = found %v, err %v", found, err)
	}
	if loaded.State.Dishes["dish-1"].Ingredients[0].Quantity != 500 {
		t.Fatal("store retained caller mutation after Save")
	}
	loaded.State.Dishes["dish-1"].Ingredients[0].Quantity = 100
	reloaded, _, err := store.LoadLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Dishes["dish-1"].Ingredients[0].Quantity != 500 {
		t.Fatal("LoadLatest exposed mutable store state")
	}

	older := productSnapshot()
	if err := store.Save(ctx, older); err != nil {
		t.Fatalf("Save(older valid candidate) error = %v", err)
	}
	latest, found, err := store.LoadLatest(ctx)
	if err != nil || !found || latest.LastSequence != 2 {
		t.Fatalf("latest after older Save() = %#v, found %v, err %v; want sequence 2", latest, found, err)
	}
}

func TestSnapshotStoreRejectsInvalidCandidateWithoutReplacingPrevious(t *testing.T) {
	store := memory.NewSnapshotStore()
	ctx := context.Background()
	if err := store.Save(ctx, validSnapshot(0)); err != nil {
		t.Fatal(err)
	}
	invalid := validSnapshot(2)
	invalid.SchemaVersion = 99
	if err := store.Save(ctx, invalid); !errors.Is(err, domain.ErrUnsupportedSchema) {
		t.Fatalf("Save(unsupported schema) error = %v; want ErrUnsupportedSchema", err)
	}
	invalid = validSnapshot(2)
	invalid.State.StateVersion = 1
	if err := store.Save(ctx, invalid); err == nil {
		t.Fatal("Save accepted state version different from snapshot sequence")
	}
	invalid = validSnapshot(2)
	invalid.State.Products = map[string]domain.Product{"product-1": {ProductID: "product-1", Name: "Tomato", BaseUnit: domain.BaseUnitG}}
	invalid.State.Dishes = map[string]domain.Dish{"dish-1": {DishID: "dish-1", Name: "Soup", Ingredients: []domain.Ingredient{{ProductID: "missing", Quantity: 500}}}}
	invalid.State.Commands["command-1"] = productCommand("command-1", "product-1")
	invalid.State.Commands["command-2"] = dishCommand("command-2", "dish-1", "missing")
	if err := store.Save(ctx, invalid); err == nil {
		t.Fatal("Save accepted a dish referencing an unknown product")
	}
	latest, found, err := store.LoadLatest(ctx)
	if err != nil || !found || latest.LastSequence != 0 {
		t.Fatalf("previous snapshot after rejected candidates = %#v, found %v, err %v; want sequence 0", latest, found, err)
	}
}

func TestSnapshotStoreCancellationBeforeCommitPreservesPrevious(t *testing.T) {
	store := memory.NewSnapshotStore()
	if err := store.Save(context.Background(), validSnapshot(0)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Save(ctx, validSnapshot(2)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save(canceled context) error = %v; want context.Canceled", err)
	}
	latest, found, err := store.LoadLatest(context.Background())
	if err != nil || !found || latest.LastSequence != 0 {
		t.Fatalf("previous snapshot after canceled Save() = %#v, found %v, err %v; want sequence 0", latest, found, err)
	}
}

func productSnapshot() domain.Snapshot {
	snapshot := validSnapshot(1)
	snapshot.State.Products["product-1"] = domain.Product{ProductID: "product-1", Name: "Tomato", BaseUnit: domain.BaseUnitG}
	snapshot.State.Commands["command-1"] = productCommand("command-1", "product-1")
	return snapshot
}

func validSnapshot(sequence int64) domain.Snapshot {
	return domain.Snapshot{
		SchemaVersion: domain.InventorySchemaV1,
		StreamID:      domain.InventoryStreamID,
		LastSequence:  sequence,
		State: domain.InventoryState{
			Products:         map[string]domain.Product{},
			Dishes:           map[string]domain.Dish{},
			Deliveries:       map[string]domain.Delivery{},
			Batches:          map[string]domain.Batch{},
			Orders:           map[string]domain.Order{},
			OrdersByProposal: map[string]string{},
			Commands:         map[string]domain.CommandRecord{},
			StateVersion:     sequence,
		},
	}
}

func productCommand(commandID, productID string) domain.CommandRecord {
	product := &domain.Product{ProductID: productID, Name: "Tomato", BaseUnit: domain.BaseUnitG}
	return domain.CommandRecord{
		CommandID: commandID, CommandType: "RegisterProduct", CommandFingerprint: commandID + "-fingerprint",
		Result: domain.CommandResult{Kind: domain.CommandResultProductRegistered, Product: product},
	}
}

func dishCommand(commandID, dishID, productID string) domain.CommandRecord {
	dish := &domain.Dish{DishID: dishID, Name: "Soup", Ingredients: []domain.Ingredient{{ProductID: productID, Quantity: 500}}}
	return domain.CommandRecord{
		CommandID: commandID, CommandType: "DefineDish", CommandFingerprint: commandID + "-fingerprint",
		Result: domain.CommandResult{Kind: domain.CommandResultDishDefined, Dish: dish},
	}
}
