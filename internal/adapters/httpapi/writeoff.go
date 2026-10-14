package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

type writeoffRuntime interface {
	ConfirmWriteoff(context.Context, contract.ConfirmWriteoffRequest) (contract.WriteoffResult, error)
	ResolveWriteoff(context.Context, string, string) (contract.WriteoffObservation, error)
}
type writeoffRequest struct {
	CommandID *string `json:"commandID"`
	BatchID   *string `json:"batchID"`
}
type writeoffWire struct {
	BatchID            string            `json:"batchID"`
	ProductID          string            `json:"productID"`
	WrittenOffQuantity string            `json:"writtenOffQuantity"`
	BaseUnit           contract.BaseUnit `json:"baseUnit"`
	ActorUserID        string            `json:"actorUserID"`
	ConfirmedAt        time.Time         `json:"confirmedAt"`
	EventID            string            `json:"eventID"`
}

func (h *scopedHandlers) writeoff(w http.ResponseWriter, r *http.Request, runtime Runtime, identity contract.Identity, operation string) {
	owner, ok := runtime.(writeoffRuntime)
	if !ok {
		writeScopedError(w, contract.ErrRuntimeNotReady, r.Method == "POST")
		return
	}
	if operation == "privateconfirmWriteoff" {
		if !noScopedQuery(w, r, true) {
			return
		}
		var input writeoffRequest
		if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		command, e1 := requiredString(input.CommandID)
		batch, e2 := requiredString(input.BatchID)
		if e1 != nil || e2 != nil || strings.HasPrefix(command, "expiration/") {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		value, err := owner.ConfirmWriteoff(r.Context(), contract.ConfirmWriteoffRequest{Scope: identity.Scope, CommandID: command, BatchID: batch, ActorUserID: identity.Subject})
		var preparation *contract.WriteoffPreparationError
		if errors.As(err, &preparation) {
			writeInvalidation(w, identity.Scope, preparation.CommittedVersion)
		}
		if err != nil {
			writeWriteoffError(w, err, true)
			return
		}
		writeInvalidation(w, identity.Scope, value.StateVersion)
		writeScopedResult(w, writeoffScope(value), value.ConfirmedAt, value.StateVersion, wireWriteoff(value))
		return
	}
	values, err := queryValues(r, "commandID", "batchID")
	if err != nil {
		writeScopedError(w, contract.ErrInvalidArgument, false)
		return
	}
	command, e1 := requiredQuery(values, "commandID")
	batch, e2 := requiredQuery(values, "batchID")
	if e1 != nil || e2 != nil {
		writeScopedError(w, contract.ErrInvalidArgument, false)
		return
	}
	value, err := owner.ResolveWriteoff(r.Context(), command, batch)
	if err != nil {
		writeWriteoffError(w, err, false)
		return
	}
	observation := struct {
		State   string              `json:"state"`
		Outcome string              `json:"outcome"`
		Result  *scopedWriteoffWire `json:"result,omitempty"`
	}{State: value.State, Outcome: "unknown"}
	if value.Result != nil {
		v := *value.Result
		result := scopedWriteoffWire{businessContextWire{v.OrganizationID, v.RestaurantID, v.RuntimeEpoch, settingsWire{v.TimeZone, v.SettingsVersion}, v.ConfirmedAt, strconv.FormatInt(v.StateVersion, 10)}, wireWriteoff(v)}
		observation.Outcome = "committed"
		observation.Result = &result
	}
	writeJSON(w, 200, struct {
		Context     businessContextWire `json:"context"`
		CommandID   string              `json:"commandID"`
		BatchID     string              `json:"batchID"`
		Fingerprint string              `json:"commandFingerprint"`
		Observation any                 `json:"observation"`
	}{businessContextWire{identity.Scope.OrganizationID, identity.Scope.RestaurantID, identity.Scope.RuntimeEpoch, settingsWire{identity.Scope.TimeZone, identity.Scope.SettingsVersion}, value.EvaluatedAt, strconv.FormatInt(value.StateVersion, 10)}, command, batch, value.Fingerprint, observation})
}

type scopedWriteoffWire struct {
	Context businessContextWire `json:"context"`
	Result  writeoffWire        `json:"result"`
}

func wireWriteoff(v contract.WriteoffResult) writeoffWire {
	return writeoffWire{v.BatchID, v.ProductID, strconv.FormatInt(int64(v.WrittenOffQuantity), 10), v.BaseUnit, v.ActorUserID, v.ConfirmedAt, v.EventID}
}
func writeoffScope(v contract.WriteoffResult) contract.Scope {
	return contract.Scope{OrganizationID: v.OrganizationID, RestaurantID: v.RestaurantID, RuntimeEpoch: v.RuntimeEpoch, TimeZone: v.TimeZone, SettingsVersion: v.SettingsVersion}
}
func writeWriteoffError(w http.ResponseWriter, err error, mutation bool) {
	switch {
	case errors.Is(err, contract.ErrAlreadyWrittenOff):
		writeScopedFailure(w, 409, "AlreadyWrittenOff", "Batch was already written off.", definiteOutcome(mutation))
	case errors.Is(err, contract.ErrNotPendingWriteoff):
		writeScopedFailure(w, 409, "NotPendingWriteoff", "Batch is not pending writeoff.", definiteOutcome(mutation))
	case errors.Is(err, contract.ErrBatchNotFound):
		writeScopedFailure(w, 404, "NotFound", "Batch does not exist.", definiteOutcome(mutation))
	default:
		writeScopedError(w, err, mutation)
	}
}
func writeInvalidation(w http.ResponseWriter, scope contract.Scope, version int64) {
	w.Header().Set("X-Umir-Committed-Version", strconv.FormatInt(version, 10))
	w.Header().Set("X-Umir-Invalidation-Required", "true")
	w.Header().Set("X-Umir-Runtime-Epoch", scope.RuntimeEpoch)
}
