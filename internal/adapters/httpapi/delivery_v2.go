package httpapi

import (
	"net/http"
	"regexp"

	"github.com/JustIkra/umir-warehouse/contract"
)

type timeInputWire struct {
	Instant *string         `json:"instant"`
	Civil   *civilInputWire `json:"civil"`
}
type civilInputWire struct {
	LocalDateTime *string `json:"localDateTime"`
	Offset        *string `json:"offset"`
	Occurrence    *string `json:"occurrence"`
}

var instantPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)
var civilPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]{1,9})?$`)

func parseTimeInput(w *timeInputWire) (contract.TimeInput, error) {
	if w == nil || (w.Instant == nil) == (w.Civil == nil) {
		return contract.TimeInput{}, contract.ErrInvalidArgument
	}
	if w.Instant != nil {
		if !instantPattern.MatchString(*w.Instant) {
			return contract.TimeInput{}, contract.ErrInvalidArgument
		}
		return contract.TimeInput{Instant: *w.Instant}, nil
	}
	c := w.Civil
	if c.LocalDateTime == nil || !civilPattern.MatchString(*c.LocalDateTime) {
		return contract.TimeInput{}, contract.ErrInvalidArgument
	}
	value := &contract.CivilInput{LocalDateTime: *c.LocalDateTime}
	if c.Offset != nil {
		value.Offset = *c.Offset
		if value.Offset == "" {
			return contract.TimeInput{}, contract.ErrInvalidArgument
		}
	}
	if c.Occurrence != nil {
		value.Occurrence = *c.Occurrence
		if value.Occurrence != "earlier" && value.Occurrence != "later" {
			return contract.TimeInput{}, contract.ErrInvalidArgument
		}
	}
	return contract.TimeInput{Civil: value}, nil
}
func (h *scopedHandlers) delivery(w http.ResponseWriter, r *http.Request, runtime Runtime, scope contract.Scope) {
	if !noScopedQuery(w, r, true) {
		return
	}
	type line struct {
		ProductID      *string           `json:"productID"`
		ManufacturedAt *timeInputWire    `json:"manufacturedAt"`
		ReceivedAt     *timeInputWire    `json:"receivedAt"`
		ShelfLife      *shelfLifeRequest `json:"shelfLife"`
		Quantity       *string           `json:"quantity"`
	}
	var input struct {
		CommandID  *string        `json:"commandID"`
		DeliveryID *string        `json:"deliveryID"`
		Number     *string        `json:"number"`
		Expected   *string        `json:"expectedSettingsVersion"`
		CreatedAt  *timeInputWire `json:"createdAt"`
		Lines      []line         `json:"lines"`
	}
	if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil || input.CommandID == nil || input.DeliveryID == nil || input.Number == nil || *input.Number == "" || input.Expected == nil || !contract.ValidVersion(*input.Expected) || len(input.Lines) < 1 || len(input.Lines) > 1000 {
		writeScopedError(w, contract.ErrInvalidArgument, true)
		return
	}
	created, err := parseTimeInput(input.CreatedAt)
	if err != nil {
		writeScopedError(w, err, true)
		return
	}
	request := contract.ReceiveDeliveryRequest{Scope: scope, CommandID: *input.CommandID, DeliveryID: *input.DeliveryID, Number: *input.Number, ExpectedSettingsVersion: *input.Expected, TimeInputs: &contract.DeliveryTimeInputs{CreatedAt: created}}
	for _, line := range input.Lines {
		m, e1 := parseTimeInput(line.ManufacturedAt)
		v, e2 := parseTimeInput(line.ReceivedAt)
		q, e3 := requiredInt64(line.Quantity)
		if line.ProductID == nil || *line.ProductID == "" || line.ShelfLife == nil || e1 != nil || e2 != nil || e3 != nil {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		life, e4 := requiredInt64(line.ShelfLife.Value)
		unit, e5 := requiredString(line.ShelfLife.Unit)
		if e4 != nil || e5 != nil {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		request.Lines = append(request.Lines, contract.DeliveryLine{ProductID: *line.ProductID, Quantity: contract.Quantity(q), ShelfLife: contract.ShelfLife{Value: life, Unit: contract.ShelfLifeUnit(unit)}})
		request.TimeInputs.Lines = append(request.TimeInputs.Lines, contract.DeliveryLineTimes{ManufacturedAt: m, ReceivedAt: v})
	}
	value, err := runtime.ReceiveDelivery(r.Context(), request)
	if err != nil {
		writeScopedError(w, err, true)
		return
	}
	if len(value.Batches) == 0 {
		writeScopedFailure(w, 503, "InventoryUnavailable", "Committed delivery context is unavailable.", "unknown")
		return
	}
	batches := make([]batchV2Wire, 0, len(value.Batches))
	for _, b := range value.Batches {
		batches = append(batches, wireBatchStatus(b, value.Scope, h.options.TZDataVersion).Batch)
		if b.PendingWriteoff || !b.ExpiresAt.After(value.EvaluatedAt) {
			writeInvalidation(w, value.Scope, value.StateVersion)
		}
	}
	writeScopedResult(w, value.Scope, value.EvaluatedAt, value.StateVersion, struct {
		DeliveryID string        `json:"deliveryID"`
		BatchIDs   []string      `json:"batchIDs"`
		Batches    []batchV2Wire `json:"batches"`
	}{value.DeliveryID, value.BatchIDs, batches})
}

func analyticsFailure(w http.ResponseWriter, code string, status int, mutation bool) {
	if code == "RefreshRequired" {
		writeJSON(w, status, struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Outcome   string `json:"outcome"`
			Retryable bool   `json:"retryable"`
			Details   struct {
				RefreshRequired bool `json:"refreshRequired"`
			} `json:"details"`
		}{Code: code, Message: "Captured report is unavailable.", Outcome: definiteOutcome(mutation), Details: struct {
			RefreshRequired bool `json:"refreshRequired"`
		}{true}})
		return
	}
	writeScopedFailure(w, status, code, "Calendar or analytics request failed.", definiteOutcome(mutation))
}
