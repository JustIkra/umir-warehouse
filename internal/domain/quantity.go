package domain

import (
	"errors"
	"math"
)

type Quantity int64

var ErrQuantityOverflow = errors.New("quantity overflow")
var ErrInvalidQuantity = errors.New("invalid quantity")

func AddQuantity(a, b Quantity) (Quantity, error) {
	left, right := int64(a), int64(b)
	if right > 0 && left > math.MaxInt64-right || right < 0 && left < math.MinInt64-right {
		return 0, ErrQuantityOverflow
	}
	return Quantity(left + right), nil
}

func MultiplyQuantity(quantity Quantity, portions int64) (Quantity, error) {
	if quantity < 0 || portions <= 0 {
		return 0, ErrInvalidQuantity
	}
	if quantity != 0 && portions > math.MaxInt64/int64(quantity) {
		return 0, ErrQuantityOverflow
	}
	return Quantity(int64(quantity) * portions), nil
}
