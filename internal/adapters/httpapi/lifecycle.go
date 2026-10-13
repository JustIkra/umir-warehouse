package httpapi

import (
	"context"
	"net/http"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/zonedata"
)

type lifecycleDirectory interface {
	RegisterRuntime(context.Context, contract.Scope, string) (contract.RuntimeReadiness, error)
	CloseRuntime(context.Context, contract.Scope, string) (contract.RuntimeReadiness, error)
}
type registerRuntimeWire struct {
	OperationID      string       `json:"operationID"`
	ExpectedEpoch    string       `json:"expectedEpoch"`
	LifecycleVersion string       `json:"lifecycleVersion"`
	Settings         settingsWire `json:"settings"`
}
type closeRuntimeWire struct {
	OperationID      string `json:"operationID"`
	ExpectedEpoch    string `json:"expectedEpoch"`
	LifecycleVersion string `json:"lifecycleVersion"`
}

func (h *scopedHandlers) lifecycle(w http.ResponseWriter, r *http.Request, scope contract.Scope, operation string) {
	if !noScopedQuery(w, r, true) {
		return
	}
	directory, ok := h.directory.(lifecycleDirectory)
	if !ok {
		writeScopedError(w, contract.ErrRuntimeNotReady, true)
		return
	}
	var input closeRuntimeWire
	if operation == "warehouseRegister" {
		var registration registerRuntimeWire
		if decodeRequest(r, h.options.MaxRequestBytes, &registration) != nil {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		input = closeRuntimeWire{registration.OperationID, registration.ExpectedEpoch, registration.LifecycleVersion}
		scope.TimeZone = registration.Settings.TimeZone
		scope.SettingsVersion = registration.Settings.Version
	} else if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil {
		writeScopedError(w, contract.ErrInvalidArgument, true)
		return
	}
	if scope.ValidateLifecycle() != nil || !contract.ValidEntityID(input.OperationID) || !contract.ValidEntityID(input.ExpectedEpoch) || !contract.ValidVersion(input.LifecycleVersion) {
		writeScopedError(w, contract.ErrInvalidArgument, true)
		return
	}
	if input.ExpectedEpoch != h.directory.Epoch() || scope.RuntimeEpoch != input.ExpectedEpoch {
		writeScopedError(w, contract.ErrRuntimeReset, true)
		return
	}
	if input.LifecycleVersion != scope.LifecycleVersion {
		writeScopedError(w, contract.ErrForbidden, true)
		return
	}
	var readiness contract.RuntimeReadiness
	var err error
	if operation == "warehouseRegister" {
		readiness, err = directory.RegisterRuntime(r.Context(), scope, input.OperationID)
	} else {
		readiness, err = directory.CloseRuntime(r.Context(), scope, input.OperationID)
	}
	if err != nil {
		writeScopedError(w, err, true)
		return
	}
	writeJSON(w, 200, readiness)
}
func (h *scopedHandlers) runtimeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || r.URL.RawQuery != "" {
		writeScopedError(w, contract.ErrInvalidArgument, false)
		return
	}
	if _, err := h.caller(r); err != nil {
		writeScopedError(w, err, false)
		return
	}
	for _, header := range []string{"Cookie", "Authorization", "X-Umir-Tenant-ID", "X-Umir-User-ID", "X-Umir-Org-ID", "X-Umir-Roles", "X-Umir-Caller", "X-Umir-Session"} {
		if len(r.Header.Values(header)) != 0 {
			writeScopedError(w, contract.ErrForbidden, false)
			return
		}
	}
	if _, err := zonedata.Load("Europe/Moscow"); err != nil {
		writeScopedError(w, contract.ErrRuntimeNotReady, false)
		return
	}
	version := h.options.TZDataVersion
	if version == "" {
		version = "unknown"
	}
	writeJSON(w, 200, struct {
		Service       string `json:"service"`
		Epoch         string `json:"epoch"`
		Ready         bool   `json:"ready"`
		TZDataVersion string `json:"tzdataVersion"`
	}{"warehouse", h.directory.Epoch(), true, version})
}
