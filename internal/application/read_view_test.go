package application

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestReadStateHasNoAliases(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	store := seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)
	snapshots := memory.NewSnapshotStore()
	owner, err := NewOwner(store, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(snapshots, 1); err != nil {
		t.Fatal(err)
	}
	view, err := owner.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	view.State.Products["p"] = domain.Product{ProductID: "p", Name: "scribbled", BaseUnit: domain.BaseUnitML}
	view.State.Products["ghost"] = domain.Product{ProductID: "ghost"}
	dish := view.State.Dishes["d"]
	dish.Name = "scribbled"
	dish.Ingredients[0].Quantity = 1
	view.State.Dishes["d"] = dish
	view.State.Dishes["ghost"] = domain.Dish{DishID: "ghost"}
	batch := view.State.Batches["batch"]
	batch.RemainingQuantity = 1
	view.State.Batches["batch"] = batch
	delete(view.State.Batches, "batch")

	again, err := owner.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.State.Products["p"].Name != "Product" || len(again.State.Products) != 1 {
		t.Fatalf("owner products changed through a returned map: %#v", again.State.Products)
	}
	if again.State.Dishes["d"].Name != "Dish" || again.State.Dishes["d"].Ingredients[0].Quantity != 6000 || len(again.State.Dishes) != 1 {
		t.Fatalf("owner dishes changed through a returned map: %#v", again.State.Dishes)
	}
	if again.State.Batches["batch"].RemainingQuantity != 10000 {
		t.Fatalf("owner batches changed through a returned map: %#v", again.State.Batches)
	}
	if worked, err := owner.RunSnapshotOnce(ctx); err != nil || !worked {
		t.Fatalf("RunSnapshotOnce() = %v, %v", worked, err)
	}
	saved, found, err := snapshots.LoadLatest(ctx)
	if err != nil || !found || saved.State.Products["p"].Name != "Product" || saved.State.Batches["batch"].RemainingQuantity != 10000 || len(saved.State.Dishes) != 1 {
		t.Fatalf("snapshot saw writes made through a returned view: %#v, found %v, err %v", saved.State, found, err)
	}

	// Concurrent writes to a returned view race with the owner's own writes
	// when the maps are shared.
	racing, err := owner.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 500 {
			racing.State.Products[fmt.Sprintf("x%d", i)] = domain.Product{}
			racing.State.Dishes[fmt.Sprintf("x%d", i)] = domain.Dish{}
			racing.State.Batches[fmt.Sprintf("x%d", i)] = domain.Batch{}
		}
	})
	for i := range 50 {
		if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: fmt.Sprintf("c%d", i), ProductID: fmt.Sprintf("q%d", i), Name: "Q", BaseUnit: contract.BaseUnitG}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
