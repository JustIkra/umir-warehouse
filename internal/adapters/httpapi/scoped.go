package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

type TenantDirectory interface {
	RuntimeFor(context.Context, contract.Scope) (Runtime, error)
	Epoch() string
}
type ScopedOptions struct {
	RequestTimeout  time.Duration
	MaxRequestBytes int64
	Verifier        Verifier
	CallerKeys      map[string]string
	TZDataVersion   string
}
type scopedHandlers struct {
	directory TenantDirectory
	options   ScopedOptions
}

func NewScopedHandler(directory TenantDirectory, options ScopedOptions) (http.Handler, error) {
	if directory == nil || !contract.ValidEntityID(directory.Epoch()) || options.Verifier == nil || options.RequestTimeout <= 0 || options.MaxRequestBytes <= 0 || options.MaxRequestBytes > 1<<20 || len(options.CallerKeys) == 0 {
		return nil, contract.ErrInvalidArgument
	}
	copied := make(map[string]string, len(options.CallerKeys))
	seen := make(map[string]bool, len(options.CallerKeys))
	for caller, key := range options.CallerKeys {
		if !slices.Contains([]string{"umir-auth", "umir-gateway", "umir-serving"}, caller) || key == "" || seen[key] {
			return nil, contract.ErrInvalidArgument
		}
		copied[caller] = key
		seen[key] = true
	}
	options.CallerKeys = copied
	return &scopedHandlers{directory, options}, nil
}

type scopedRoute struct {
	operation     string
	epochRequired bool
}

func (r scopedRoute) mutation() bool {
	switch r.operation {
	case "privateregisterProduct", "privatedefineDish", "privatereceiveDelivery", "privateconfirmWriteoff", "dueExpiry", "warehouseRegister", "warehouseClose", "consumerCommit":
		return true
	default:
		return false
	}
}

func scopedRouteFor(method, path string) (scopedRoute, bool) {
	parts := strings.Split(path, "/")
	if method == "GET" && len(parts) == 3 && parts[0] == "products" && parts[1] != "" && parts[2] == "batches" {
		return scopedRoute{"privateProductBatches", false}, true
	}
	routes := map[string]scopedRoute{
		"GET supply-suggestions": {"privateSupplySuggestions", false}, "GET recommendation-inputs": {"consumerRecommendations", false},
		"GET products": {"privateproducts", false}, "POST products": {"privateregisterProduct", true},
		"GET dish-catalog": {"privatedishCatalog", false}, "GET dish": {"privatedishDetails", false}, "POST dishes": {"privatedefineDish", true},
		"POST deliveries": {"privatereceiveDelivery", true}, "GET inventory": {"privateinventory", false}, "GET expiring-batches": {"privateexpiringBatches", false}, "GET history": {"privatehistory", false},
		"POST writeoffs": {"privateconfirmWriteoff", true}, "GET id-suggestions": {"privatesuggestID", false}, "GET analytics/stock": {"privateAnalyticsstock", false}, "GET analytics/movements": {"privateAnalyticsmovements", false},
		"GET pending-writeoffs": {"privatePendingWriteoffs", false}, "GET writeoffs/commands": {"privateresolveWriteoffCommand", true},
		"POST expiry-runs": {"dueExpiry", true}, "PUT registry": {"warehouseRegister", false}, "DELETE registry": {"warehouseClose", false},
		"GET menu-evaluation": {"consumerMenu", false}, "POST order-evaluations": {"consumerEvaluate", true}, "POST order-commits": {"consumerCommit", true},
		"GET commands": {"consumerCommand", true}, "GET orders": {"consumerOrder", true}, "GET order-attempts": {"consumerAttempt", true},
	}
	route, ok := routes[method+" "+path]
	return route, ok
}
func (h *scopedHandlers) caller(r *http.Request) (string, error) {
	values := r.Header.Values("X-Umir-Service-Key")
	if len(values) != 1 || values[0] == "" {
		return "", contract.ErrUnauthenticated
	}
	caller := ""
	for name, key := range h.options.CallerKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(values[0])) == 1 {
			caller = name
		}
	}
	if caller == "" {
		return "", contract.ErrUnauthenticated
	}
	return caller, nil
}
func (h *scopedHandlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/healthz" {
		if r.Method != "GET" || r.URL.RawQuery != "" {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
		writeJSON(w, 200, struct {
			Status  string `json:"status"`
			Service string `json:"service"`
		}{"ok", "umir-warehouse"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.options.RequestTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	if r.URL.Path == "/internal/warehouse/v2/runtime" {
		h.runtimeStatus(w, r)
		return
	}
	prefix := "/internal/warehouse/v2/r/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeScopedError(w, contract.ErrInvalidArgument, r.Method != "GET")
		return
	}
	segments := strings.SplitN(strings.TrimPrefix(r.URL.Path, prefix), "/", 2)
	if len(segments) != 2 {
		writeScopedError(w, contract.ErrInvalidArgument, r.Method != "GET")
		return
	}
	route, ok := scopedRouteFor(r.Method, segments[1])
	encodedSegments := strings.SplitN(strings.TrimPrefix(r.URL.EscapedPath(), prefix), "/", 2)
	if len(encodedSegments) == 2 {
		if encoded, matched := scopedRouteFor(r.Method, encodedSegments[1]); matched && encoded.operation == "privateProductBatches" {
			if encodedSegments[0] != segments[0] {
				writeScopedError(w, contract.ErrInvalidArgument, false)
				return
			}
			route, ok = encoded, true
		}
	}
	if !contract.ValidEntityID(segments[0]) {
		mutation := r.Method != "GET"
		if ok {
			mutation = route.mutation()
		}
		writeScopedError(w, contract.ErrInvalidArgument, mutation)
		return
	}
	if !ok {
		writeScopedFailure(w, 405, "MethodNotAllowed", "Route or method is not supported.", definiteOutcome(r.Method != "GET"))
		return
	}
	caller, err := h.caller(r)
	if err != nil {
		writeScopedError(w, err, route.mutation())
		return
	}
	for _, header := range []string{"X-Umir-Tenant-ID", "X-Umir-User-ID", "X-Umir-Org-ID", "X-Umir-Roles", "X-Umir-Caller", "X-Umir-Session", "Cookie"} {
		if len(r.Header.Values(header)) != 0 {
			writeScopedError(w, contract.ErrForbidden, route.mutation())
			return
		}
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") || strings.ContainsAny(strings.TrimPrefix(auth[0], "Bearer "), " \t\n\r") {
		writeScopedError(w, contract.ErrInvalidToken, route.mutation())
		return
	}
	epochs := r.Header.Values("X-Umir-Runtime-Epoch")
	expected := h.directory.Epoch()
	if len(epochs) > 1 || (route.epochRequired && (len(epochs) != 1 || !contract.ValidEntityID(epochs[0]))) {
		writeScopedError(w, contract.ErrInvalidArgument, route.mutation())
		return
	}
	identity, err := h.options.Verifier.Validate(ctx, strings.TrimPrefix(auth[0], "Bearer "), caller, segments[0], route.operation, expected)
	if err != nil {
		writeScopedError(w, err, route.mutation())
		return
	}
	// The injected verifier is a trust boundary, but cannot override route identity.
	policy, allowed := operationPolicy(route.operation, caller)
	if !allowed || identity.Scope.RestaurantID != segments[0] || identity.Kind != policy.kind || !slices.Contains(identity.Permissions, policy.capability) || !slices.Contains(policy.operations, identity.Operation) {
		writeScopedError(w, contract.ErrForbidden, route.mutation())
		return
	}
	if route.epochRequired && epochs[0] != expected {
		writeScopedError(w, contract.ErrRuntimeReset, false)
		return
	}
	if route.operation == "warehouseRegister" || route.operation == "warehouseClose" {
		h.lifecycle(w, r, identity.Scope, route.operation)
		return
	}
	runtime, err := h.directory.RuntimeFor(ctx, identity.Scope)
	if err != nil {
		writeScopedError(w, err, route.mutation())
		return
	}
	h.dispatch(w, r, runtime, identity, route.operation)
}

type settingsWire struct {
	TimeZone string `json:"timeZone"`
	Version  string `json:"settingsVersion"`
}
type businessContextWire struct {
	OrganizationID string       `json:"orgID"`
	TenantID       string       `json:"tenantID"`
	RuntimeEpoch   string       `json:"runtimeEpoch"`
	Settings       settingsWire `json:"settings"`
	EvaluatedAt    time.Time    `json:"evaluatedAt"`
	StateVersion   string       `json:"stateVersion"`
}

func writeScopedResult(w http.ResponseWriter, scope contract.Scope, at time.Time, version int64, value any) {
	writeJSON(w, 200, struct {
		Context businessContextWire `json:"context"`
		Result  any                 `json:"result"`
	}{businessContextWire{scope.OrganizationID, scope.RestaurantID, scope.RuntimeEpoch, settingsWire{scope.TimeZone, scope.SettingsVersion}, at, strconv.FormatInt(version, 10)}, value})
}
func (h *scopedHandlers) dispatch(w http.ResponseWriter, r *http.Request, runtime Runtime, identity contract.Identity, operation string) {
	ctx := r.Context()
	scope := identity.Scope
	mutation := scopedRoute{operation: operation}.mutation()
	switch operation {
	case "privateSupplySuggestions", "consumerRecommendations":
		h.planning(w, r, runtime, identity, operation)
	case "privateProductBatches":
		h.productBatches(w, r, runtime, identity)
	case "consumerCommit", "consumerOrder", "consumerAttempt":
		h.scopedOrder(w, r, runtime, scope, operation)
	case "privatehistory":
		h.scopedHistory(w, r, runtime, scope)
	case "privateexpiringBatches":
		h.scopedExpiring(w, r, runtime, scope)
	case "privatesuggestID":
		values, err := queryValues(r, "kind")
		if err != nil {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
		kind, err := requiredQuery(values, "kind")
		if err != nil {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
		value, err := runtime.SuggestID(ctx, contract.CatalogKind(kind))
		if err != nil {
			writeScopedError(w, err, false)
			return
		}
		writeScopedResult(w, scope, value.EvaluatedAt, value.StateVersion, struct {
			Kind        contract.CatalogKind `json:"kind"`
			SuggestedID string               `json:"suggestedID"`
			Reserved    bool                 `json:"reserved"`
		}{value.Kind, value.SuggestedID, value.Reserved})
	case "privateAnalyticsstock", "privateAnalyticsmovements":
		h.analytics(w, r, runtime, identity, operation)
	case "privatereceiveDelivery":
		h.delivery(w, r, runtime, scope)
	case "privateinventory", "privatePendingWriteoffs":
		h.stock(w, r, runtime, scope, operation)
	case "privateconfirmWriteoff", "privateresolveWriteoffCommand":
		h.writeoff(w, r, runtime, identity, operation)
	case "dueExpiry":
		if !noScopedQuery(w, r, true) {
			return
		}
		var input struct {
			RunID *string `json:"runID"`
		}
		if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil || input.RunID == nil || !contract.ValidEntityID(*input.RunID) {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		value, err := runtime.RunExpirations(ctx)
		if len(value.Expired) > 0 {
			writeInvalidation(w, value.Scope, value.StateVersion)
		}
		if err != nil {
			writeScopedError(w, err, true)
			return
		}
		scope = value.Scope
		ids := make([]string, 0, len(value.Expired))
		for _, v := range value.Expired {
			ids = append(ids, v.BatchID)
		}
		writeScopedResult(w, scope, value.EvaluatedAt, value.StateVersion, struct {
			IDs          []string `json:"transitionedBatchIDs"`
			Final        int      `json:"finalWriteoffs"`
			Version      string   `json:"committedVersion"`
			Invalidation bool     `json:"invalidationRequired"`
		}{ids, 0, strconv.FormatInt(value.StateVersion, 10), len(ids) > 0})
	case "privateproducts":
		if noScopedQuery(w, r, mutation) {
			value, err := runtime.GetProducts(ctx)
			if err != nil {
				writeScopedError(w, err, false)
				return
			}
			writeScopedResult(w, scope, value.EvaluatedAt, value.StateVersion, wireProducts(value))
		}
	case "privatedishCatalog":
		if noScopedQuery(w, r, mutation) {
			value, err := runtime.GetDishCatalog(ctx)
			if err != nil {
				writeScopedError(w, err, false)
				return
			}
			writeScopedResult(w, scope, value.EvaluatedAt, value.StateVersion, wireDishCatalog(value))
		}
	case "privatedishDetails":
		values, err := queryValues(r, "dishID")
		if err != nil {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
		id, err := requiredQuery(values, "dishID")
		if err != nil {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
		value, err := runtime.GetDish(ctx, id)
		if err != nil {
			writeScopedError(w, err, false)
			return
		}
		writeScopedResult(w, scope, value.EvaluatedAt, value.StateVersion, wireDishDetails(value))
	case "privateregisterProduct":
		if !noScopedQuery(w, r, mutation) {
			return
		}
		var input productRequest
		if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		command, e1 := requiredString(input.CommandID)
		id, e2 := requiredString(input.ProductID)
		name, e3 := requiredString(input.Name)
		unit, e4 := requiredString(input.BaseUnit)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || id == "*" || strings.HasPrefix(command, "expiration/") {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		value, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: command, ProductID: id, Name: name, BaseUnit: contract.BaseUnit(unit)})
		if err != nil {
			writeScopedError(w, err, true)
			return
		}
		snapshot, err := runtime.GetProducts(ctx)
		if err != nil {
			writeScopedFailure(w, 503, "InventoryUnavailable", "Committed result context is unavailable.", "unknown")
			return
		}
		writeScopedResult(w, scope, snapshot.EvaluatedAt, snapshot.StateVersion, wireProduct(value))
	case "privatedefineDish":
		if !noScopedQuery(w, r, mutation) {
			return
		}
		var input dishRequest
		if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		request, err := parseDish(input)
		if err != nil || request.DishID == "*" {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		value, err := runtime.DefineDish(ctx, request)
		if err != nil {
			writeScopedError(w, err, true)
			return
		}
		snapshot, err := runtime.GetDishCatalog(ctx)
		if err != nil {
			writeScopedFailure(w, 503, "InventoryUnavailable", "Committed result context is unavailable.", "unknown")
			return
		}
		writeScopedResult(w, scope, snapshot.EvaluatedAt, snapshot.StateVersion, wireDish(value, true))
	case "consumerMenu":
		if noScopedQuery(w, r, mutation) {
			value, err := runtime.EvaluateMenu(ctx)
			if err != nil {
				writeScopedError(w, err, false)
				return
			}
			writeScopedResult(w, scope, value.EvaluatedAt, value.StateVersion, wireMenuEvaluation(value))
		}
	case "consumerEvaluate":
		if !noScopedQuery(w, r, mutation) {
			return
		}
		var input orderEvaluationRequest
		if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
		positions, err := parsePositions(input.Positions)
		if err != nil {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
		value, err := runtime.EvaluateOrder(ctx, contract.EvaluateOrderRequest{Positions: positions})
		if err != nil {
			writeScopedError(w, err, false)
			return
		}
		writeScopedResult(w, scope, value.EvaluatedAt, value.StateVersion, wireEvaluation(value))
	case "consumerCommand":
		values, err := queryValues(r, "commandID")
		if err != nil {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
		id, err := requiredQuery(values, "commandID")
		if err != nil {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
		value, found, err := runtime.FindRecordedCommand(ctx, id)
		if err != nil {
			writeScopedError(w, err, false)
			return
		}
		if found && value.Result.Kind == contract.CommandResultBatchWriteoffConfirmed {
			found = false
		}
		response := foundCommandWire{Found: found}
		if found {
			record := wireCommandRecord(value)
			response.Record = &record
		}
		head, err := runtime.Head(ctx)
		if err != nil {
			writeScopedError(w, err, false)
			return
		}
		writeScopedResult(w, scope, head.EvaluatedAt, head.StateVersion, response)
	default:
		writeScopedFailure(w, 503, "ServiceUnavailable", "This operation is not installed.", definiteOutcome(mutation))
	}
}
func noScopedQuery(w http.ResponseWriter, r *http.Request, mutation bool) bool {
	values, err := queryValues(r)
	if err != nil || len(values) != 0 {
		writeScopedError(w, contract.ErrInvalidArgument, mutation)
		return false
	}
	return true
}
func writeScopedFailure(w http.ResponseWriter, status int, code, message, outcome string) {
	writeJSON(w, status, struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Outcome   string `json:"outcome"`
		Retryable bool   `json:"retryable"`
	}{code, message, outcome, false})
}
func writeScopedError(w http.ResponseWriter, err error, mutation bool) {
	for _, entry := range []struct {
		err    error
		code   string
		status int
	}{{contract.ErrCalculationUnavailable, "CalculationUnavailable", 503}, {contract.ErrSettingsChanged, "SettingsChanged", 409}, {contract.ErrUnknownTimeZone, "UnknownTimeZone", 422}, {contract.ErrTimeZoneDataUnavailable, "TimeZoneDataUnavailable", 503}, {contract.ErrAmbiguousLocalTime, "AmbiguousLocalTime", 422}, {contract.ErrNonexistentLocalTime, "NonexistentLocalTime", 422}, {contract.ErrSkippedDate, "SkippedDate", 422}, {contract.ErrRefreshRequired, "RefreshRequired", 409}, {contract.ErrUnknownEventSchema, "UnknownEventSchema", 503}, {contract.ErrLimitExceeded, "LimitExceeded", 503}} {
		if errors.Is(err, entry.err) {
			analyticsFailure(w, entry.code, entry.status, mutation)
			return
		}
	}
	status, code, message, outcome := classifyError(err, mutation)
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		status = 401
		code = "Unauthenticated"
	case errors.Is(err, contract.ErrInvalidToken):
		status = 401
		code = "InvalidToken"
	case errors.Is(err, contract.ErrForbidden):
		status = 403
		code = "Forbidden"
	case errors.Is(err, contract.ErrAuthUnavailable):
		status = 503
		code = "AuthUnavailable"
	case errors.Is(err, contract.ErrScopeDeleted):
		status = 410
		code = "ScopeDeleted"
	case errors.Is(err, contract.ErrRuntimeNotReady):
		status = 503
		code = "RuntimeNotReady"
	case errors.Is(err, contract.ErrRuntimeReset):
		status = 409
		code = "RuntimeReset"
		outcome = "indeterminate"
	default:
		switch code {
		case "ProductAlreadyExists":
			code = "ProductConflict"
		case "DishAlreadyExists":
			code = "DishConflict"
		case "CommandIdentityConflict", "DeliveryAlreadyReceived":
			code = "CommandConflict"
		case "InternalError", "InsufficientInventory":
			code = "TechnicalError"
		case "ProductNotFound", "DishNotFound":
			code = "NotFound"
		}
		writeScopedFailure(w, status, code, message, outcome)
		return
	}
	if code != "RuntimeReset" {
		outcome = definiteOutcome(mutation)
	}
	writeScopedFailure(w, status, code, "Request trust or restaurant readiness check failed.", outcome)
}
