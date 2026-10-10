package domain

import (
	"strings"
	"testing"
	"time"
)

func TestAcceptanceShelfLifeDayIsTwentyFourHours(t *testing.T) {
	manufacturedAt := time.Date(2026, time.March, 7, 13, 45, 12, 345, time.FixedZone("MSK", 3*60*60))
	got, err := CalculateExpiresAt(manufacturedAt, ShelfLife{Value: 1, Unit: ShelfLifeDay})
	if err != nil {
		t.Fatalf("CalculateExpiresAt(+1 day) error: %v", err)
	}
	want := manufacturedAt.Add(24 * time.Hour)
	if !got.Equal(want) || got.Sub(manufacturedAt) != 24*time.Hour {
		t.Fatalf("one day expiry = %s, elapsed %s; want %s and 24h", got.Format(time.RFC3339Nano), got.Sub(manufacturedAt), want.Format(time.RFC3339Nano))
	}
}

func TestAcceptanceShelfLifeMonthUsesLeapYearEnd(t *testing.T) {
	manufacturedAt := time.Date(2024, time.January, 31, 12, 30, 15, 250, time.FixedZone("MSK", 3*60*60))
	got, err := CalculateExpiresAt(manufacturedAt, ShelfLife{Value: 1, Unit: ShelfLifeMonth})
	if err != nil {
		t.Fatalf("CalculateExpiresAt(+1 month in leap year) error: %v", err)
	}
	want := time.Date(2024, time.February, 29, 12, 30, 15, 250, manufacturedAt.Location())
	if !got.Equal(want) || got.Format(time.RFC3339Nano) != "2024-02-29T12:30:15.00000025+03:00" {
		t.Fatalf("leap-year month expiry = %s; want %s", got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}

func TestAcceptanceCanonicalBusinessTimeRemovesActualMonotonicReading(t *testing.T) {
	withMonotonic := time.Now()
	if !strings.Contains(withMonotonic.String(), "m=") {
		t.Fatal("time.Now() input did not carry a monotonic reading")
	}
	withoutMonotonic := withMonotonic.Round(0)
	canonical := CanonicalBusinessTime(withMonotonic)
	if got, want := CanonicalBusinessTimeString(withMonotonic), CanonicalBusinessTimeString(withoutMonotonic); got != want {
		t.Fatalf("canonical time with monotonic data = %q; without = %q", got, want)
	}
	if canonical.Location().String() != "MSK" || !canonical.Equal(withoutMonotonic) || strings.Contains(canonical.String(), "m=") {
		t.Fatalf("canonical value = %s; want Moscow wall time without monotonic data", canonical)
	}
}
