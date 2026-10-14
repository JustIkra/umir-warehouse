package domain

import (
	"errors"
	"testing"
	"time"
)

func TestW5CalendarDSTAndSkippedBoundaries(t *testing.T) {
	berlin, err := LoadZone("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		date  string
		hours int
	}{{"2026-03-29", 23}, {"2026-10-25", 25}} {
		start, err := DateBoundary(test.date, berlin)
		if err != nil {
			t.Fatal(err)
		}
		d, err := time.Parse("2006-01-02", test.date)
		if err != nil {
			t.Fatal(err)
		}
		end, err := DateBoundary(d.AddDate(0, 0, 1).Format("2006-01-02"), berlin)
		if err != nil {
			t.Fatal(err)
		}
		if end.Sub(start) != time.Duration(test.hours)*time.Hour {
			t.Fatalf("%s=%v", test.date, end.Sub(start))
		}
	}
	apia, err := LoadZone("Pacific/Apia")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DateBoundary("2011-12-30", apia); !errors.Is(err, ErrSkippedDate) {
		t.Fatalf("skipped date=%v", err)
	}
	havana, err := LoadZone("America/Havana")
	if err != nil {
		t.Fatal(err)
	}
	start, err := DateBoundary("2026-11-01", havana)
	if err != nil {
		t.Fatal(err)
	}
	_, offset := start.Zone()
	if offset != -4*3600 {
		t.Fatalf("repeated midnight uses later offset=%d", offset)
	}
	start, err = DateBoundary("2026-03-08", havana)
	if err != nil {
		t.Fatal(err)
	}
	if start.Hour() != 1 {
		t.Fatalf("midnight gap normalized to %v", start)
	}
}
func TestW5ManualCivilFoldGapAndOffsets(t *testing.T) {
	zone, err := LoadZone("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		text, offset, occurrence string
		want                     error
	}{{"2026-03-29T02:30:00", "", "", ErrNonexistentLocalTime}, {"2026-10-25T02:30:00", "", "", ErrAmbiguousLocalTime}, {"2026-10-25T02:30:00", "+01:00", "earlier", ErrInvalidBusinessTime}, {"2026-04-31T10:00:00", "", "", ErrInvalidBusinessTime}} {
		if _, err := ResolveCivil(test.text, test.offset, test.occurrence, zone); !errors.Is(err, test.want) {
			t.Fatalf("%+v = %v", test, err)
		}
	}
	earlier, err := ResolveCivil("2026-10-25T02:30:00", "+02:00", "earlier", zone)
	if err != nil {
		t.Fatal(err)
	}
	later, err := ResolveCivil("2026-10-25T02:30:00", "+01:00", "later", zone)
	if err != nil {
		t.Fatal(err)
	}
	if later.Sub(earlier) != time.Hour {
		t.Fatalf("fold delta=%v", later.Sub(earlier))
	}
}
func TestW5ShelfLifeCalendarMonthAndPhysicalDay(t *testing.T) {
	zone, err := LoadZone("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		start string
		life  ShelfLife
		want  string
	}{{"2026-01-31T12:30:00", ShelfLife{1, ShelfLifeMonth}, "2026-02-28T12:30:00+01:00"}, {"2026-02-28T02:30:00", ShelfLife{1, ShelfLifeMonth}, "2026-03-28T02:30:00+01:00"}, {"2026-01-29T02:30:00", ShelfLife{2, ShelfLifeMonth}, "2026-03-29T03:00:00+02:00"}, {"2026-08-25T02:30:00", ShelfLife{2, ShelfLifeMonth}, "2026-10-25T02:30:00+02:00"}, {"2026-03-28T12:00:00", ShelfLife{1, ShelfLifeDay}, "2026-03-29T13:00:00+02:00"}} {
		start, err := ResolveCivil(test.start, "", "", zone)
		if err != nil {
			t.Fatal(err)
		}
		expiry, err := CalculateExpiresAtInZone(start, test.life, zone)
		if err != nil {
			t.Fatal(err)
		}
		if got := expiry.In(zone).Format(time.RFC3339); got != test.want {
			t.Fatalf("%+v got=%s", test, got)
		}
	}
	if _, err := CalculateExpiresAtInZone(moscowTimeForCalendar(), ShelfLife{120000, ShelfLifeMonth}, zone); !errors.Is(err, ErrShelfLifeOverflow) {
		t.Fatalf("month overflow=%v", err)
	}
}
func moscowTimeForCalendar() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.FixedZone("MSK", 3*3600))
}
