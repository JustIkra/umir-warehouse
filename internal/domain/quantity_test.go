package domain

import (
	"math"
	"testing"
)

func TestQuantityOverflow(t *testing.T) {
	got, err := AddQuantity(Quantity(40000), Quantity(500))
	if err != nil || got != Quantity(40500) {
		t.Fatalf("AddQuantity(40000, 500) = %d, %v; want 40500, nil", got, err)
	}

	if _, err := AddQuantity(Quantity(math.MaxInt64), Quantity(1)); err == nil {
		t.Fatal("AddQuantity(MaxInt64, 1) succeeded; want overflow error")
	}
	if _, err := MultiplyQuantity(Quantity(math.MaxInt64), 2); err == nil {
		t.Fatal("MultiplyQuantity(MaxInt64, 2) succeeded; want overflow error")
	}
}

func TestMultiplyQuantityRejectsZeroInput(t *testing.T) {
	if _, err := MultiplyQuantity(0, 1); err == nil {
		t.Fatal("MultiplyQuantity(0, 1) succeeded; want invalid quantity error")
	}
}
