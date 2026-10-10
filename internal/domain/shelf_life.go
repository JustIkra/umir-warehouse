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
		return manufacturedAt.Add(time.Duration(life.Value * day)), nil
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
		lastDay := time.Date(int(targetYear), targetMonth+1, 0, 0, 0, 0, 0, manufacturedAt.Location()).Day()
		if day > lastDay {
			day = lastDay
		}
		y, m, d := int(targetYear), targetMonth, day
		hour, minute, second := manufacturedAt.Clock()
		return time.Date(y, m, d, hour, minute, second, manufacturedAt.Nanosecond(), manufacturedAt.Location()), nil
	default:
		return time.Time{}, ErrInvalidShelfLife
	}
}
