package domain

import (
	"errors"
	"math"
	"time"
)

type ShelfLifeUnit string

const (
	ShelfLifeDay   ShelfLifeUnit = "day"
	ShelfLifeMonth ShelfLifeUnit = "month"
)

type ShelfLife struct {
	Value int64
	Unit  ShelfLifeUnit
}

var ErrInvalidShelfLife = errors.New("invalid shelf life")
var ErrInvalidBusinessTime = errors.New("business time must use UTC+03:00")
var ErrShelfLifeOverflow = errors.New("shelf life overflow")

func CalculateExpiresAt(manufacturedAt time.Time, life ShelfLife) (time.Time, error) {
	if life.Value <= 0 {
		return time.Time{}, ErrInvalidShelfLife
	}
	_, offset := manufacturedAt.Zone()
	if offset != 3*60*60 {
		return time.Time{}, ErrInvalidBusinessTime
	}

	switch life.Unit {
	case ShelfLifeDay:
		day := int64(24 * time.Hour)
		if life.Value > math.MaxInt64/day {
			return time.Time{}, ErrShelfLifeOverflow
		}
		duration := time.Duration(life.Value * day)
		year, month, monthDay := manufacturedAt.Date()
		hour, minute, second := manufacturedAt.Clock()
		_, offset := manufacturedAt.Zone()
		startSeconds, ok := civilUnixSeconds(int64(year), month, monthDay, hour, minute, second, offset)
		if !ok || startSeconds != manufacturedAt.Unix() {
			return time.Time{}, ErrShelfLifeOverflow
		}
		expectedSeconds, ok := checkedAddInt64(startSeconds, int64(duration/time.Second))
		if !ok {
			return time.Time{}, ErrShelfLifeOverflow
		}
		expiresAt := manufacturedAt.Add(duration)
		if !expiresAt.After(manufacturedAt) || expiresAt.Sub(manufacturedAt) != duration || expiresAt.Unix() != expectedSeconds {
			return time.Time{}, ErrShelfLifeOverflow
		}
		return expiresAt, nil
	case ShelfLifeMonth:
		year, month, day := manufacturedAt.Date()
		monthIndex := int64(year)*12 + int64(month-1)
		if monthIndex > math.MaxInt64-life.Value {
			return time.Time{}, ErrShelfLifeOverflow
		}
		targetIndex := monthIndex + life.Value
		targetYear := targetIndex / 12
		monthRemainder := targetIndex % 12
		if monthRemainder < 0 {
			targetYear--
			monthRemainder += 12
		}
		targetMonth := time.Month(monthRemainder + 1)
		if targetYear > math.MaxInt || targetYear < math.MinInt {
			return time.Time{}, ErrShelfLifeOverflow
		}
		lastDay := daysInMonth(targetYear, targetMonth)
		if day > lastDay {
			day = lastDay
		}
		y, m, d := int(targetYear), targetMonth, day
		hour, minute, second := manufacturedAt.Clock()
		expiresAt := time.Date(y, m, d, hour, minute, second, manufacturedAt.Nanosecond(), manufacturedAt.Location())
		actualYear, actualMonth, actualDay := expiresAt.Date()
		actualHour, actualMinute, actualSecond := expiresAt.Clock()
		if actualYear != y || actualMonth != m || actualDay != d || actualHour != hour || actualMinute != minute || actualSecond != second || expiresAt.Nanosecond() != manufacturedAt.Nanosecond() {
			return time.Time{}, ErrShelfLifeOverflow
		}
		_, offset := expiresAt.Zone()
		expectedSeconds, ok := civilUnixSeconds(int64(y), m, d, hour, minute, second, offset)
		if !ok || expiresAt.Unix() != expectedSeconds {
			return time.Time{}, ErrShelfLifeOverflow
		}
		return expiresAt, nil
	default:
		return time.Time{}, ErrInvalidShelfLife
	}
}

func daysInMonth(year int64, month time.Month) int {
	switch month {
	case time.April, time.June, time.September, time.November:
		return 30
	case time.February:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	default:
		return 31
	}
}

func civilUnixSeconds(year int64, month time.Month, day, hour, minute, second, offset int) (int64, bool) {
	if month <= time.February {
		year--
	}
	era := year / 400
	yearOfEra := year % 400
	if yearOfEra < 0 {
		era--
		yearOfEra += 400
	}
	monthIndex := int64(month)
	if month > time.February {
		monthIndex -= 3
	} else {
		monthIndex += 9
	}
	dayOfYear := (153*monthIndex+2)/5 + int64(day) - 1
	daysOfEra := yearOfEra*365 + yearOfEra/4 - yearOfEra/100 + dayOfYear
	days, ok := checkedMulInt64(era, 146097)
	if !ok {
		return 0, false
	}
	days, ok = checkedAddInt64(days, daysOfEra-719468)
	if !ok {
		return 0, false
	}
	seconds, ok := checkedMulInt64(days, 86400)
	if !ok {
		return 0, false
	}
	daySeconds := int64(hour*3600 + minute*60 + second - offset)
	return checkedAddInt64(seconds, daySeconds)
}

func checkedAddInt64(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b || b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}

func checkedMulInt64(a, b int64) (int64, bool) {
	if a > 0 && b > 0 && a > math.MaxInt64/b || a < 0 && b > 0 && a < math.MinInt64/b {
		return 0, false
	}
	return a * b, true
}
