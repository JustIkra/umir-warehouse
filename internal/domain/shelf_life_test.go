package domain

import (
	"testing"
	"time"
)

func TestShelfLifeMonthEnd(t *testing.T) {
	manufacturedAt := time.Date(2026, time.January, 31, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))

	oneMonth, err := CalculateExpiresAt(manufacturedAt, ShelfLife{Value: 1, Unit: ShelfLifeMonth})
	if err != nil {
		t.Fatalf("CalculateExpiresAt(+1 month) error: %v", err)
	}
	wantOneMonth := time.Date(2026, time.February, 28, 12, 0, 0, 0, manufacturedAt.Location())
	if !oneMonth.Equal(wantOneMonth) || oneMonth.Format(time.RFC3339) != "2026-02-28T12:00:00+03:00" {
		t.Fatalf("one month expiry = %s; want 2026-02-28T12:00:00+03:00", oneMonth.Format(time.RFC3339))
	}

	twoMonths, err := CalculateExpiresAt(manufacturedAt, ShelfLife{Value: 2, Unit: ShelfLifeMonth})
	if err != nil {
		t.Fatalf("CalculateExpiresAt(+2 months) error: %v", err)
	}
	if got := twoMonths.Format(time.RFC3339); got != "2026-03-31T12:00:00+03:00" {
		t.Fatalf("two month expiry = %s; want 2026-03-31T12:00:00+03:00", got)
	}
}

func TestBusinessMidnightValid(t *testing.T) {
	midnight := time.Date(2026, time.January, 31, 0, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	if _, err := CalculateExpiresAt(midnight, ShelfLife{Value: 1, Unit: ShelfLifeDay}); err != nil {
		t.Fatalf("full business datetime at midnight was rejected: %v", err)
	}

	unsupportedOffset := time.Date(2026, time.January, 31, 0, 0, 0, 0, time.FixedZone("unsupported", 4*60*60))
	if _, err := CalculateExpiresAt(unsupportedOffset, ShelfLife{Value: 1, Unit: ShelfLifeDay}); err == nil {
		t.Fatal("business datetime with +04:00 offset was accepted")
	}
}

func TestShelfLifeMonthBeforeYearZero(t *testing.T) {
	location := time.FixedZone("MSK", 3*60*60)
	manufacturedAt := time.Date(-1, time.January, 31, 12, 0, 0, 0, location)

	got, err := CalculateExpiresAt(manufacturedAt, ShelfLife{Value: 1, Unit: ShelfLifeMonth})
	if err != nil {
		t.Fatalf("CalculateExpiresAt(+1 month) error: %v", err)
	}
	want := time.Date(-1, time.February, 28, 12, 0, 0, 0, location)
	if !got.Equal(want) {
		t.Fatalf("expiry = %s; want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}
