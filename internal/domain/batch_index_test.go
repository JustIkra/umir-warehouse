package domain

import (
	"math/rand"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// indexedEvaluationState holds batchCount batches spread evenly over
// productCount products and one dish using the first ingredientCount products.
// The state is cloned so it carries the index a running owner has.
func indexedEvaluationState(batchCount, productCount, ingredientCount int) InventoryState {
	moscow := time.FixedZone("MSK", 3*60*60)
	base := time.Date(2026, time.January, 5, 9, 0, 0, 0, moscow)
	state := InventoryState{
		Products:     map[string]Product{},
		Dishes:       map[string]Dish{},
		DeliveryIDs:  map[string]struct{}{},
		Batches:      make(map[string]Batch, batchCount),
		StateVersion: 1,
	}
	dish := Dish{DishID: "dish", Name: "Dish"}
	for p := range productCount {
		id := "product-" + strconv.Itoa(p)
		state.Products[id] = Product{ProductID: id, Name: id, BaseUnit: BaseUnitG}
		if p < ingredientCount {
			dish.Ingredients = append(dish.Ingredients, Ingredient{ProductID: id, Quantity: 100})
		}
	}
	state.Dishes[dish.DishID] = dish
	for i := range batchCount {
		id := "batch-" + strconv.Itoa(i)
		expires := base.Add(time.Duration(i) * time.Hour)
		state.Batches[id] = Batch{
			BatchID: id, DeliveryID: "delivery", LineNo: int64(i + 1), ProductID: "product-" + strconv.Itoa(i%productCount),
			ManufacturedAt: base, ShelfLife: ShelfLife{Value: 12, Unit: ShelfLifeMonth}, ReceivedAt: base, ExpiresAt: expires,
			ReceivedQuantity: 1_000_000, RemainingQuantity: 1_000_000,
		}
	}
	return CloneInventoryState(state)
}

// Evaluating an order touches only the batches of its ingredients, so the
// number of allocations must not follow the number of batches in the state.
func TestEvaluateAllocsIndependentOfBatchCount(t *testing.T) {
	positions := []Position{{LineNo: 1, DishID: "dish", Portions: 2}}
	allocs := func(batchCount int) float64 {
		state := indexedEvaluationState(batchCount, 20, 4)
		return testing.AllocsPerRun(20, func() {
			if _, _, err := EvaluatePositionsWithAllocations(state, positions); err != nil {
				t.Fatal(err)
			}
		})
	}
	small, large := allocs(200), allocs(2000)
	if large > small*1.1 {
		t.Fatalf("allocations grew from %.0f with 200 batches to %.0f with 2000; want within 10%%", small, large)
	}
}

func randomBatchState(random *rand.Rand) (InventoryState, []Position) {
	productCount := 1 + random.Intn(6)
	state := indexedEvaluationState(0, productCount, 0)
	state.Dishes = map[string]Dish{}
	for d := range 1 + random.Intn(4) {
		dish := Dish{DishID: "dish-" + strconv.Itoa(d), Name: "Dish"}
		for _, p := range random.Perm(productCount)[:1+random.Intn(productCount)] {
			dish.Ingredients = append(dish.Ingredients, Ingredient{ProductID: "product-" + strconv.Itoa(p), Quantity: Quantity(1 + random.Intn(300))})
		}
		state.Dishes[dish.DishID] = dish
	}
	moscow := time.FixedZone("MSK", 3*60*60)
	base := time.Date(2026, time.January, 5, 9, 0, 0, 0, moscow)
	for i := range random.Intn(40) {
		id := "batch-" + strconv.Itoa(i)
		remaining := Quantity(random.Intn(700))
		state.Batches[id] = Batch{
			BatchID: id, ProductID: "product-" + strconv.Itoa(random.Intn(productCount)),
			ReceivedAt: base.Add(time.Duration(random.Intn(3)) * time.Hour), ExpiresAt: base.Add(time.Duration(random.Intn(5)) * 24 * time.Hour),
			ReceivedQuantity: remaining, RemainingQuantity: remaining, PendingWriteoff: random.Intn(6) == 0,
		}
	}
	positions := make([]Position, 0, 3)
	for line := range 1 + random.Intn(3) {
		positions = append(positions, Position{LineNo: int64(line + 1), DishID: "dish-" + strconv.Itoa(random.Intn(len(state.Dishes))), Portions: int64(1 + random.Intn(4))})
	}
	return state, positions
}

type evaluationResult struct {
	Evaluation  OrderEvaluation
	Allocations []Allocation
	Err         error
}

func evaluate(state InventoryState, positions []Position) evaluationResult {
	evaluation, allocations, err := EvaluatePositionsWithAllocations(state, positions)
	return evaluationResult{evaluation, allocations, err}
}

// The index only narrows the search: every way of building a state gives the
// result of the full scan.
func TestEvaluateWithIndexEqualsFullScan(t *testing.T) {
	for seed := range int64(300) {
		random := rand.New(rand.NewSource(seed))
		cloned, positions := randomBatchState(random)
		literal := cloned
		literal.byProduct, literal.indexed = nil, 0
		want := evaluate(literal, positions)

		if got := evaluate(cloned, positions); !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: clone\n got %+v\nwant %+v", seed, got, want)
		}
		if got := evaluate(CloneReadView(literal), positions); !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: read view\n got %+v\nwant %+v", seed, got, want)
		}

		// Merge in two steps, the second replacing some batches of the first.
		merged := InventoryState{Products: literal.Products, Dishes: literal.Dishes}
		first, second := map[string]Batch{}, map[string]Batch{}
		for id, batch := range literal.Batches {
			if random.Intn(2) == 0 {
				first[id] = batch
			} else {
				second[id] = batch
			}
		}
		for id, batch := range first {
			if random.Intn(3) == 0 {
				second[id] = batch
			}
		}
		merged.Merge(StateDelta{Batches: first})
		merged.Merge(StateDelta{Batches: second, StateVersion: literal.StateVersion})
		if got := evaluate(merged, positions); !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: merge\n got %+v\nwant %+v", seed, got, want)
		}
		if len(literal.Batches) > 0 && merged.indexed != len(merged.Batches) {
			t.Fatalf("seed %d: merged index counts %d batches; state has %d", seed, merged.indexed, len(merged.Batches))
		}
	}
}

// A batch added behind the index's back must not be missed.
func TestEvaluateFallsBackWhenIndexIsStale(t *testing.T) {
	state := indexedEvaluationState(10, 2, 2)
	batch := state.Batches["batch-0"]
	batch.BatchID, batch.ExpiresAt = "batch-extra", batch.ExpiresAt.Add(-24*time.Hour)
	state.Batches[batch.BatchID] = batch
	_, allocations, err := EvaluatePositionsWithAllocations(state, []Position{{LineNo: 1, DishID: "dish", Portions: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if allocations[0].BatchID != "batch-extra" {
		t.Fatalf("first allocation = %q; want the earliest-expiring batch-extra", allocations[0].BatchID)
	}
}
