package domain

import (
	"errors"
	"sort"
	"time"
)

var (
	ErrUnknownTimeZone      = errors.New("unknown IANA time zone")
	ErrAmbiguousLocalTime   = errors.New("ambiguous local time")
	ErrNonexistentLocalTime = errors.New("nonexistent local time")
	ErrSkippedDate          = errors.New("skipped calendar date")
)

func LoadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, ErrUnknownTimeZone
	}
	zone, err := time.LoadLocation(name)
	if err != nil {
		return nil, ErrUnknownTimeZone
	}
	return zone, nil
}

func civilCandidates(civil time.Time, zone *time.Location) ([]time.Time, time.Time) {
	offsets := map[int]bool{}
	transitions := map[time.Time]bool{}
	for h := -72; h <= 72; h++ {
		local := civil.Add(time.Duration(h) * time.Hour).In(zone)
		_, offset := local.Zone()
		offsets[offset] = true
		_, end := local.ZoneBounds()
		if !end.IsZero() {
			transitions[end] = true
		}
	}
	candidates := make([]time.Time, 0, 2)
	for offset := range offsets {
		candidate := civil.Add(-time.Duration(offset) * time.Second)
		if sameCivil(candidate.In(zone), civil) {
			candidates = append(candidates, candidate.In(zone))
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	var gapEnd time.Time
	for transition := range transitions {
		before := civilValue(transition.Add(-time.Nanosecond).In(zone))
		after := civilValue(transition.In(zone))
		if civil.After(before) && civil.Before(after) && (gapEnd.IsZero() || transition.Before(gapEnd)) {
			gapEnd = transition.In(zone)
		}
	}
	return candidates, gapEnd
}
func civilValue(t time.Time) time.Time {
	y, m, d := t.Date()
	h, n, s := t.Clock()
	return time.Date(y, m, d, h, n, s, t.Nanosecond(), time.UTC)
}
func sameCivil(a, b time.Time) bool { return civilValue(a).Equal(civilValue(b)) }

func ResolveCivil(text, offset, occurrence string, zone *time.Location) (time.Time, error) {
	civil, err := time.Parse("2006-01-02T15:04:05.999999999", text)
	if err != nil || len(text) < 19 || civil.Year() < 1 || civil.Year() > 9999 || zone == nil {
		return time.Time{}, ErrInvalidBusinessTime
	}
	if occurrence != "" && occurrence != "earlier" && occurrence != "later" {
		return time.Time{}, ErrInvalidBusinessTime
	}
	candidates, _ := civilCandidates(civil, zone)
	if len(candidates) == 0 {
		return time.Time{}, ErrNonexistentLocalTime
	}
	selected := -1
	if offset != "" {
		parsed, err := time.Parse("-07:00", offset)
		if err != nil || len(offset) != 6 {
			return time.Time{}, ErrInvalidBusinessTime
		}
		_, wanted := parsed.Zone()
		for i, candidate := range candidates {
			_, actual := candidate.Zone()
			if actual == wanted {
				selected = i
			}
		}
		if selected < 0 {
			return time.Time{}, ErrInvalidBusinessTime
		}
	}
	if occurrence != "" {
		wanted := 0
		if occurrence == "later" {
			wanted = len(candidates) - 1
		}
		if selected >= 0 && selected != wanted {
			return time.Time{}, ErrInvalidBusinessTime
		}
		selected = wanted
	}
	if selected < 0 {
		if len(candidates) > 1 {
			return time.Time{}, ErrAmbiguousLocalTime
		}
		selected = 0
	}
	return candidates[selected], nil
}
func DateBoundary(date string, zone *time.Location) (time.Time, error) {
	civil, err := time.Parse("2006-01-02", date)
	if err != nil || len(date) != 10 || civil.Year() < 1 || civil.Year() > 9999 || zone == nil {
		return time.Time{}, ErrInvalidBusinessTime
	}
	candidates, gapEnd := civilCandidates(civil, zone)
	if len(candidates) > 0 {
		return candidates[0], nil
	}
	if !gapEnd.IsZero() && gapEnd.In(zone).Format("2006-01-02") == date {
		return gapEnd, nil
	}
	return time.Time{}, ErrSkippedDate
}
func CalculateExpiresAtInZone(manufacturedAt time.Time, life ShelfLife, zone *time.Location) (time.Time, error) {
	if zone == nil || manufacturedAt.IsZero() {
		return time.Time{}, ErrInvalidBusinessTime
	}
	if life.Unit == ShelfLifeDay {
		return CalculateExpiresAt(CanonicalBusinessTime(manufacturedAt), life)
	}
	if life.Unit != ShelfLifeMonth || life.Value <= 0 {
		return time.Time{}, ErrInvalidShelfLife
	}
	local := manufacturedAt.In(zone)
	y, m, d := local.Date()
	if life.Value > int64(9999-y)*12+int64(12-m) {
		return time.Time{}, ErrShelfLifeOverflow
	}
	targetIndex := int64(y)*12 + int64(m-1) + life.Value
	ty, tm := int(targetIndex/12), time.Month(targetIndex%12+1)
	d = min(d, daysInMonth(int64(ty), tm))
	h, n, s := local.Clock()
	civil := time.Date(ty, tm, d, h, n, s, local.Nanosecond(), time.UTC)
	candidates, gapEnd := civilCandidates(civil, zone)
	if len(candidates) > 0 {
		return CanonicalBusinessTime(candidates[0]), nil
	}
	if !gapEnd.IsZero() {
		return CanonicalBusinessTime(gapEnd), nil
	}
	return time.Time{}, ErrShelfLifeOverflow
}
