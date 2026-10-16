package httpapi

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

// historyPageSize is the most events one history response carries.
const historyPageSize = 500

type journalRecordWire struct {
	TenantID     string          `json:"tenantID"`
	RuntimeEpoch string          `json:"runtimeEpoch"`
	Event        scopedEventWire `json:"event"`
}
type scopedEventWire struct {
	EventID            string             `json:"eventID"`
	StreamID           string             `json:"streamID"`
	Sequence           string             `json:"sequence"`
	SchemaVersion      int64              `json:"schemaVersion"`
	CommandID          string             `json:"commandID"`
	CommandFingerprint string             `json:"commandFingerprint"`
	RecordedAt         string             `json:"recordedAt"`
	Type               contract.EventType `json:"type"`
	TenantID           string             `json:"tenantID,omitempty"`
	RuntimeEpoch       string             `json:"runtimeEpoch,omitempty"`
	Payload            any                `json:"payload"`
}

func (h *scopedHandlers) scopedHistory(w http.ResponseWriter, r *http.Request, runtime Runtime, scope contract.Scope) {
	values, err := queryValues(r, "fromSequence")
	if err != nil {
		writeScopedError(w, contract.ErrInvalidArgument, false)
		return
	}
	from := int64(0)
	if text, ok := values["fromSequence"]; ok {
		if len(text) != 1 {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
		from, err = strconv.ParseInt(text[0], 10, 64)
		if err != nil || from < 0 || strconv.FormatInt(from, 10) != text[0] {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
	}
	history, err := runtime.HistoryPage(r.Context(), from, historyPageSize)
	if err != nil {
		writeScopedError(w, err, false)
		return
	}
	records := make([]journalRecordWire, 0, len(history.Events))
	for _, event := range history.Events {
		records = append(records, journalRecordWire{scope.RestaurantID, scope.RuntimeEpoch, wireScopedEvent(event, scope, h.options.TZDataVersion)})
	}
	writeScopedResult(w, scope, history.EvaluatedAt, history.StateVersion, struct {
		Events []journalRecordWire `json:"events"`
	}{records})
}
func wireScopedEvent(event contract.Event, scope contract.Scope, tzdata string) scopedEventWire {
	legacy := wireHistory(contract.History{Events: []contract.Event{event}}).Events[0]
	result := scopedEventWire{EventID: legacy.EventID, StreamID: legacy.StreamID, Sequence: legacy.Sequence, SchemaVersion: legacy.SchemaVersion, CommandID: legacy.CommandID, CommandFingerprint: legacy.CommandFingerprint, RecordedAt: legacy.RecordedAt, Type: legacy.Type, Payload: legacy.Payload}
	switch {
	case event.Payload.Pending != nil:
		v := event.Payload.Pending
		result.TenantID = event.TenantID
		result.RuntimeEpoch = event.RuntimeEpoch
		result.Payload = struct {
			BatchID   string            `json:"batchID"`
			ProductID string            `json:"productID"`
			Remaining string            `json:"remainingQuantity"`
			Unit      contract.BaseUnit `json:"baseUnit"`
			ExpiresAt time.Time         `json:"expiresAt"`
		}{v.BatchID, v.ProductID, strconv.FormatInt(int64(v.RemainingQuantity), 10), v.BaseUnit, v.ExpiresAt}
	case event.Payload.Writeoff != nil:
		result.TenantID = event.TenantID
		result.RuntimeEpoch = event.RuntimeEpoch
		result.Payload = wireWriteoff(*event.Payload.Writeoff)
	case event.SchemaVersion == 2 && event.Payload.Delivery != nil:
		batches := make([]batchV2Wire, 0, len(event.Payload.Batches))
		for _, b := range event.Payload.Batches {
			batches = append(batches, wireBatchStatus(b, scope, tzdata).Batch)
		}
		result.Payload = struct {
			Delivery deliveryWire  `json:"delivery"`
			Batches  []batchV2Wire `json:"batches"`
		}{wireDelivery(*event.Payload.Delivery), batches}
	}
	return result
}
func (h *scopedHandlers) scopedExpiring(w http.ResponseWriter, r *http.Request, runtime Runtime, scope contract.Scope) {
	if !noScopedQuery(w, r, false) {
		return
	}
	owner, ok := runtime.(stockRuntime)
	if !ok {
		writeScopedError(w, contract.ErrRuntimeNotReady, false)
		return
	}
	prepared, err := owner.PrepareStock(r.Context())
	if prepared.InvalidationRequired {
		writeInvalidation(w, scope, prepared.StateVersion)
	}
	if err != nil {
		writeScopedError(w, err, false)
		return
	}
	value := contract.ExpiringBatches{EvaluatedAt: prepared.EvaluatedAt, StateVersion: prepared.StateVersion, Items: make([]contract.ExpiringBatch, 0)}
	units := make(map[string]contract.BaseUnit, len(prepared.Products))
	for _, p := range prepared.Products {
		units[p.ProductID] = p.BaseUnit
	}
	cutoff := prepared.EvaluatedAt.Add(48 * time.Hour)
	for _, b := range prepared.Batches {
		if b.PendingWriteoff || b.RemainingQuantity <= 0 || b.ExpiresAt.After(cutoff) {
			continue
		}
		value.Items = append(value.Items, contract.ExpiringBatch{BatchID: b.BatchID, ProductID: b.ProductID, RemainingQuantity: b.RemainingQuantity, BaseUnit: units[b.ProductID], ExpiresAt: b.ExpiresAt, RemainingSeconds: int64(b.ExpiresAt.Sub(prepared.EvaluatedAt) / time.Second)})
	}
	sort.Slice(value.Items, func(i, j int) bool {
		a, b := value.Items[i], value.Items[j]
		if !a.ExpiresAt.Equal(b.ExpiresAt) {
			return a.ExpiresAt.Before(b.ExpiresAt)
		}
		return a.BatchID < b.BatchID
	})
	writeScopedResult(w, scope, prepared.EvaluatedAt, prepared.StateVersion, wireExpiringBatches(value))
}
