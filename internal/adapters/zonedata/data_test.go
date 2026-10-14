package zonedata

import (
	"errors"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

func TestW5ExplicitPackagedZoneDataIgnoresHostFallback(t *testing.T) {
	t.Setenv("ZONEINFO", t.TempDir())
	zone, err := Load("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2026, time.March, 29, 1, 0, 0, 0, time.UTC).In(zone)
	if instant.Hour() != 3 {
		t.Fatalf("packaged transition=%v", instant)
	}
	if _, err = Load("Local"); !errors.Is(err, contract.ErrUnknownTimeZone) {
		t.Fatalf("Local=%v", err)
	}
	if _, err = Load("Missing/Zone"); !errors.Is(err, contract.ErrUnknownTimeZone) {
		t.Fatalf("unknown=%v", err)
	}
	original := data
	data = []byte("invalid archive")
	t.Cleanup(func() { data = original })
	if err = Check(); !errors.Is(err, contract.ErrTimeZoneDataUnavailable) {
		t.Fatalf("missing/corrupt data silently falls back=%v", err)
	}
}
