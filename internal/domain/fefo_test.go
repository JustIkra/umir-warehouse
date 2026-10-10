package domain

import (
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
