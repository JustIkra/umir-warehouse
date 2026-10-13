package application

import (
	"context"
	"errors"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func calendarError(err error) error {
	switch {
	case errors.Is(err, contract.ErrTimeZoneDataUnavailable):
		return contract.ErrTimeZoneDataUnavailable
	case errors.Is(err, contract.ErrUnknownTimeZone), errors.Is(err, domain.ErrUnknownTimeZone):
		return contract.ErrUnknownTimeZone
	case errors.Is(err, domain.ErrAmbiguousLocalTime):
		return contract.ErrAmbiguousLocalTime
	case errors.Is(err, domain.ErrNonexistentLocalTime):
		return contract.ErrNonexistentLocalTime
	case errors.Is(err, domain.ErrSkippedDate):
		return contract.ErrSkippedDate
	default:
		return contract.ErrInvalidArgument
	}
}
func resolveTime(input contract.TimeInput, zone *time.Location) (time.Time, error) {
	if (input.Instant == "") == (input.Civil == nil) {
		return time.Time{}, contract.ErrInvalidArgument
	}
	if input.Civil != nil {
		c := input.Civil
		value, err := domain.ResolveCivil(c.LocalDateTime, c.Offset, c.Occurrence, zone)
		if err != nil {
			return time.Time{}, calendarError(err)
		}
		return domain.CanonicalBusinessTime(value), nil
	}
	value, err := time.Parse(time.RFC3339Nano, input.Instant)
	if err != nil || len(input.Instant) < 20 || value.Year() < 1 || value.Year() > 9999 {
		return time.Time{}, contract.ErrInvalidArgument
	}
	return domain.CanonicalBusinessTime(value), nil
}
func resolveDeliveryTimes(ctx context.Context, request contract.ReceiveDeliveryRequest, loadZone func(string) (*time.Location, error)) (contract.ReceiveDeliveryRequest, error) {
	zone, err := loadZone(request.Scope.TimeZone)
	if err != nil {
		return request, calendarError(err)
	}
	inputs := request.TimeInputs
	if inputs == nil {
		return request, nil
	}
	if len(inputs.Lines) != len(request.Lines) {
		return request, contract.ErrInvalidArgument
	}
	request.CreatedAt, err = resolveTime(inputs.CreatedAt, zone)
	if err != nil {
		return request, err
	}
	request.Lines = append([]contract.DeliveryLine(nil), request.Lines...)
	for i, line := range inputs.Lines {
		if err := ctx.Err(); err != nil {
			return request, err
		}
		request.Lines[i].ManufacturedAt, err = resolveTime(line.ManufacturedAt, zone)
		if err != nil {
			return request, err
		}
		request.Lines[i].ReceivedAt, err = resolveTime(line.ReceivedAt, zone)
		if err != nil {
			return request, err
		}
	}
	return request, nil
}
