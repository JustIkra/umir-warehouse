package domain

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestFEFOTies(t *testing.T) {
	location := time.FixedZone("MSK", 3*60*60)
	expiresAt := time.Date(2026, time.March, 10, 12, 0, 0, 0, location)
	batches := []Batch{
		{BatchID: "b", ProductID: "product", ExpiresAt: expiresAt, ReceivedAt: time.Date(2026, time.January, 2, 9, 0, 0, 0, location), RemainingQuantity: Quantity(1000)},
		{BatchID: "z", ProductID: "product", ExpiresAt: expiresAt, ReceivedAt: time.Date(2026, time.January, 1, 9, 0, 0, 0, location), RemainingQuantity: Quantity(1000)},
		{BatchID: "a", ProductID: "product", ExpiresAt: expiresAt, ReceivedAt: time.Date(2026, time.January, 1, 9, 0, 0, 0, location), RemainingQuantity: Quantity(1000)},
	}

	got, err := AllocateFEFO(batches, Quantity(3000))
	if err != nil {
		t.Fatalf("AllocateFEFO error: %v", err)
	}
	want := []Allocation{
		{ProductID: "product", BatchID: "a", Quantity: Quantity(1000)},
		{ProductID: "product", BatchID: "z", Quantity: Quantity(1000)},
		{ProductID: "product", BatchID: "b", Quantity: Quantity(1000)},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d allocations; want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allocation %d = %#v; want %#v", i, got[i], want[i])
		}
	}
}

func TestFEFOMultipleBatches(t *testing.T) {
	location := time.FixedZone("MSK", 3*60*60)
	batches := []Batch{
		{BatchID: "later", ProductID: "product", ExpiresAt: time.Date(2026, time.April, 1, 0, 0, 0, 0, location), RemainingQuantity: Quantity(900)},
		{BatchID: "earlier", ProductID: "product", ExpiresAt: time.Date(2026, time.March, 1, 0, 0, 0, 0, location), RemainingQuantity: Quantity(600)},
	}

	got, err := AllocateFEFO(batches, Quantity(1000))
	if err != nil {
		t.Fatalf("AllocateFEFO error: %v", err)
	}
	want := []Allocation{
		{ProductID: "product", BatchID: "earlier", Quantity: Quantity(600)},
		{ProductID: "product", BatchID: "later", Quantity: Quantity(400)},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d allocations; want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allocation %d = %#v; want %#v", i, got[i], want[i])
		}
	}

	batches[1].RemainingQuantity = Quantity(0)
	if got[1].Quantity != Quantity(400) {
		t.Fatalf("allocation changed after input mutation: %#v", got[1])
	}
}

func TestPureEvaluatePositionsPreservesLineNumbersAndAllocations(t *testing.T) {
	location := time.FixedZone("MSK", 3*60*60)
	expired := time.Date(2026, time.March, 1, 0, 0, 0, 0, location)
	later := expired.Add(24 * time.Hour)
	state := InventoryState{
		Products: map[string]Product{"p": {ProductID: "p", Name: "Pear", BaseUnit: BaseUnitG}},
		Dishes: map[string]Dish{
			"a": {DishID: "a", Name: "A", Ingredients: []Ingredient{{ProductID: "p", Quantity: 700}}},
			"b": {DishID: "b", Name: "B", Ingredients: []Ingredient{{ProductID: "p", Quantity: 700}}},
		},
		Batches: map[string]Batch{
			"old": {BatchID: "old", ProductID: "p", ExpiresAt: expired, RemainingQuantity: 400},
			"new": {BatchID: "new", ProductID: "p", ExpiresAt: later, RemainingQuantity: 400},
		},
		StateVersion: 12,
	}
	got, err := pureEvaluatePositions(state, []Position{{LineNo: 3, DishID: "a", Portions: 1}, {LineNo: 8, DishID: "b", Portions: 1}})
	if err != nil {
		t.Fatal(err)
	}
	want := OrderEvaluation{
		StateVersion:       12,
		RequestedPositions: []Position{{LineNo: 3, DishID: "a", Portions: 1}, {LineNo: 8, DishID: "b", Portions: 1}},
		AcceptedPositions:  []AcceptedPosition{{LineNo: 3, DishID: "a", Portions: 1}},
		Changes:            []Change{{LineNo: 8, DishID: "b", RequestedPortions: 1, AcceptedPortions: 0, Deficits: []Deficit{{ProductID: "p", RequiredQuantity: 700, AvailableQuantity: 100}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pureEvaluatePositions() = %#v; want %#v", got, want)
	}
}

func TestPureEvaluatePositionsRejectsOverflow(t *testing.T) {
	tests := []struct {
		name  string
		state InventoryState
		input []Position
	}{
		{
			name: "available quantity sum",
			state: InventoryState{
				Dishes: map[string]Dish{"d": {DishID: "d", Ingredients: []Ingredient{{ProductID: "p", Quantity: 1}}}},
				Batches: map[string]Batch{
					"a": {BatchID: "a", ProductID: "p", RemainingQuantity: Quantity(math.MaxInt64)},
					"b": {BatchID: "b", ProductID: "p", RemainingQuantity: 1},
				},
			},
			input: []Position{{LineNo: 1, DishID: "d", Portions: 1}},
		},
		{
			name: "required quantity multiplication",
			state: InventoryState{
				Dishes:  map[string]Dish{"d": {DishID: "d", Ingredients: []Ingredient{{ProductID: "p", Quantity: 2}}}},
				Batches: map[string]Batch{"a": {BatchID: "a", ProductID: "p", RemainingQuantity: Quantity(math.MaxInt64)}},
			},
			input: []Position{{LineNo: 1, DishID: "d", Portions: math.MaxInt64}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := pureEvaluatePositions(test.state, test.input)
			if !errors.Is(err, ErrQuantityOverflow) {
				t.Fatalf("pureEvaluatePositions() error = %v; want ErrQuantityOverflow", err)
			}
			if len(got.AcceptedPositions) != 0 || len(got.Changes) != 0 {
				t.Fatalf("overflow returned partial result %#v", got)
			}
		})
	}
}
