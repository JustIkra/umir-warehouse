package domain

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestFINSupplyThresholdUsesExactCoverage(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	for _, available := range []Quantity{2999, 3000, 3001} {
		state := InventoryState{Products: map[string]Product{"p": {ProductID: "p", BaseUnit: BaseUnitG}}, Batches: map[string]Batch{"b": {BatchID: "b", ProductID: "p", RemainingQuantity: available, ExpiresAt: at.Add(time.Hour)}}}
		rows, err := CalculateSupply(state, map[string]Quantity{"p": 7000}, at)
		if err != nil {
			t.Fatal(err)
		}
		if available == 2999 {
			if len(rows) != 1 || rows[0].SuggestedQuantity != 4001 || !rows[0].LowCoverage {
				t.Fatalf("below threshold: %+v", rows)
			}
		} else if len(rows) != 0 {
			t.Fatalf("coverage >=3 incorrectly suggested: %+v", rows)
		}
	}
}

func TestFINSupplyExcludesExpiredPendingAndUsesMaximumDeficit(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	state := InventoryState{
		Products: map[string]Product{"p": {ProductID: "p", BaseUnit: BaseUnitML}},
		Dishes: map[string]Dish{
			"a": {DishID: "a", Ingredients: []Ingredient{{ProductID: "p", Quantity: 5000}}},
			"b": {DishID: "b", Ingredients: []Ingredient{{ProductID: "p", Quantity: 7000}}},
		},
		Batches: map[string]Batch{
			"good":    {ProductID: "p", RemainingQuantity: 2000, ExpiresAt: at.Add(time.Hour)},
			"expired": {ProductID: "p", RemainingQuantity: 9000, ExpiresAt: at},
			"pending": {ProductID: "p", RemainingQuantity: 9000, ExpiresAt: at.Add(time.Hour), PendingWriteoff: true},
		},
	}
	rows, err := CalculateSupply(state, nil, at)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%+v: %v", rows, err)
	}
	row := rows[0]
	if row.AvailableQuantity != 2000 || row.OnePortionDeficit != 5000 || row.SuggestedQuantity != 5000 || row.LowCoverage || len(row.BlockedDishIDs) != 2 || row.BlockedDishIDs[0] != "a" || row.BlockedDishIDs[1] != "b" {
		t.Fatalf("wrong no-history/eligibility/maximum result: %+v", row)
	}
	if state.Batches["expired"].PendingWriteoff || state.Batches["good"].RemainingQuantity != 2000 {
		t.Fatal("read mutated source")
	}
}

func TestFINSupplyChecksOverflowAndKeepsProductUnitsSeparate(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	state := InventoryState{Products: map[string]Product{"g": {ProductID: "g", BaseUnit: BaseUnitG}, "ml": {ProductID: "ml", BaseUnit: BaseUnitML}}, Batches: map[string]Batch{}}
	rows, err := CalculateSupply(state, map[string]Quantity{"g": 1, "ml": math.MaxInt64}, at)
	if err != nil || len(rows) != 2 || rows[0].SuggestedQuantity != 1 || rows[1].SuggestedQuantity != math.MaxInt64 {
		t.Fatalf("units/quantum/intermediate overflow: %+v %v", rows, err)
	}
	state.Batches = map[string]Batch{"a": {ProductID: "g", RemainingQuantity: math.MaxInt64, ExpiresAt: at.Add(time.Hour)}, "b": {ProductID: "g", RemainingQuantity: 1, ExpiresAt: at.Add(time.Hour)}}
	if _, err := CalculateSupply(state, nil, at); !errors.Is(err, ErrQuantityOverflow) {
		t.Fatalf("missing overflow: %v", err)
	}
}
