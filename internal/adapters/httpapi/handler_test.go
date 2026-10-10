package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/httpapi"
)

func TestHTTPHandlersSeparatePublicAndInternalRoutes(t *testing.T) {
	runtime, err := bootstrap.NewInMemory(bootstrap.Options{Now: func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60)) }, NewID: func() string { return "id" }, EventsPerSnapshot: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	public, internal, err := bootstrap.NewHTTPHandlers(runtime, bootstrap.HTTPOptions{RequestTimeout: time.Second, MaxRequestBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	publicResponse := httptest.NewRecorder()
	public.ServeHTTP(publicResponse, httptest.NewRequest(http.MethodGet, "/internal/v1/products", nil))
	if publicResponse.Code != http.StatusNotFound {
		t.Fatalf("public internal-path status = %d, want 404", publicResponse.Code)
	}
	internalResponse := httptest.NewRecorder()
	internal.ServeHTTP(internalResponse, httptest.NewRequest(http.MethodGet, "/internal/v1/order-attempts?commandID=c1&proposalID=p1", nil))
	if internalResponse.Code != http.StatusOK || internalResponse.Body.String() != "{\"state\":\"Unseen\"}\n" {
		t.Fatalf("internal attempt response = %d %s", internalResponse.Code, internalResponse.Body.String())
	}
}

func TestHTTPHandlersRejectDuplicateFieldsBeforeMutation(t *testing.T) {
	runtime, err := bootstrap.NewInMemory(bootstrap.Options{Now: time.Now, NewID: func() string { return "id" }, EventsPerSnapshot: 10})
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := bootstrap.NewHTTPHandlers(runtime, bootstrap.HTTPOptions{RequestTimeout: time.Second, MaxRequestBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/deliveries", strings.NewReader(`{"commandID":"c1","commandID":"c2"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	public.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate-field status = %d, want 400", response.Code)
	}
	history, err := runtime.GetHistory(context.Background())
	if err != nil || len(history.Events) != 0 {
		t.Fatalf("duplicate-field request mutated warehouse: events=%d err=%v", len(history.Events), err)
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("error media type = %q", response.Header().Get("Content-Type"))
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["code"] != "InvalidArgument" || body["outcome"] != "not_committed" {
		t.Fatalf("error body = %s, %v", response.Body.String(), err)
	}
}

func TestWarehouseHTTPRoutesPreserveWireContract(t *testing.T) {
	runtime := newRuntime(t)
	public, internal := newHandlers(t, runtime)
	if got := getJSON(t, internal, "/internal/v1/products"); len(arrayField(t, got, "items")) != 0 {
		t.Fatalf("empty products response = %#v", got)
	}
	if got := getJSON(t, internal, "/internal/v1/dish-catalog"); len(arrayField(t, got, "items")) != 0 {
		t.Fatalf("empty dish catalog response = %#v", got)
	}
	dishID := "суп/+ &?"
	postJSON(t, internal, "/internal/v1/products", `{"commandID":"product-command","productID":"сыр/+ &?","name":"Молоко","baseUnit":"ml"}`, http.StatusOK)
	postJSON(t, internal, "/internal/v1/dishes", `{"commandID":"dish-command","dishID":"суп/+ &?","name":"Суп","ingredients":[{"productID":"сыр/+ &?","quantity":"1"}]}`, http.StatusOK)
	dishPath := "/internal/v1/dish?" + url.Values{"dishID": []string{dishID}}.Encode()
	details := getJSON(t, internal, dishPath)
	dishDetails := objectField(t, details["dish"], "dish")
	if dishDetails["ingredients"] == nil {
		t.Fatalf("dish detail response omitted ingredients: %#v", details)
	}
	storedRecipe := objectField(t, arrayField(t, dishDetails, "ingredients")[0], "ingredient")
	if dishDetails["dishID"] != dishID || storedRecipe["baseUnit"] != "ml" {
		t.Fatalf("dish detail response = %#v", details)
	}
	menu := getJSON(t, internal, "/internal/v1/menu-evaluation")
	if objectField(t, arrayField(t, menu, "dishes")[0], "menu dish")["dishID"] != dishID {
		t.Fatalf("menu response = %#v", menu)
	}
	lineNo := "9007199254740993"
	evaluation := postJSON(t, internal, "/internal/v1/order-evaluations", `{"positions":[{"lineNo":"9007199254740993","dishID":"суп/+ &?","portions":"2"}]}`, http.StatusOK)
	requested := objectField(t, arrayField(t, evaluation, "requestedPositions")[0], "requested position")
	if requested["lineNo"] != lineNo {
		t.Fatalf("lineNo lost precision: %#v", evaluation)
	}
	deliveryBody := `{"commandID":"delivery-command","deliveryID":"поставка/+ &?","number":"D-1","createdAt":"2026-10-11T11:59:59.123456789+03:00","lines":[{"productID":"сыр/+ &?","manufacturedAt":"2026-10-10T12:00:00+03:00","shelfLife":{"value":"3","unit":"day"},"receivedAt":"2026-10-11T12:00:00+03:00","quantity":"9007199254740993"}]}`
	postJSON(t, public, "/deliveries", deliveryBody, http.StatusOK)
	evaluation = postJSON(t, internal, "/internal/v1/order-evaluations", `{"positions":[{"lineNo":"9007199254740993","dishID":"суп/+ &?","portions":"2"}]}`, http.StatusOK)
	accepted := objectField(t, arrayField(t, evaluation, "acceptedPositions")[0], "accepted position")
	if accepted["lineNo"] != lineNo || accepted["portions"] != "2" {
		t.Fatalf("accepted position = %#v", accepted)
	}
	commitBody := `{"commandID":"order-command","proposalID":"proposal-1","acceptedPositions":[{"lineNo":"9007199254740993","dishID":"суп/+ &?","portions":"2"}]}`
	commit := postJSON(t, internal, "/internal/v1/order-commits", commitBody, http.StatusOK)
	if commit["kind"] != "OrderCommitted" || objectField(t, commit["order"], "order")["placedAt"] != "2026-10-11T12:00:00+03:00" {
		t.Fatalf("commit wire response = %#v", commit)
	}
	command := getJSON(t, internal, "/internal/v1/commands?commandID=product-command")
	commandRecord := objectField(t, command["record"], "record")
	if command["found"] != true || objectField(t, commandRecord["result"], "result")["kind"] != "ProductRegistered" {
		t.Fatalf("recorded command = %#v", command)
	}
	dishCommand := getJSON(t, internal, "/internal/v1/commands?commandID=dish-command")
	dishRecord := objectField(t, dishCommand["record"], "record")
	dishResult := objectField(t, dishRecord["result"], "result")
	recordedDish := objectField(t, dishResult["dish"], "dish")
	recordedDishIngredient := objectField(t, arrayField(t, recordedDish, "ingredients")[0], "recorded ingredient")
	if _, present := recordedDishIngredient["baseUnit"]; present {
		t.Fatalf("recorded dish incorrectly restored optional baseUnit: %#v", recordedDishIngredient)
	}
	order := getJSON(t, internal, "/internal/v1/orders?proposalID=proposal-1")
	if order["found"] != true || objectField(t, order["order"], "order")["proposalID"] != "proposal-1" {
		t.Fatalf("recorded order = %#v", order)
	}
	attempt := getJSON(t, internal, "/internal/v1/order-attempts?commandID=order-command&proposalID=proposal-1")
	if attempt["state"] != "Completed" || objectField(t, attempt["result"], "attempt result")["kind"] != "OrderCommitted" {
		t.Fatalf("attempt = %#v", attempt)
	}
	inventory := getJSON(t, public, "/inventory")
	remaining := objectField(t, arrayField(t, inventory, "products")[0], "inventory product")["quantity"]
	if remaining != "9007199254740991" {
		t.Fatalf("remaining quantity = %v, want exact large integer", remaining)
	}
	expiring := getJSON(t, public, "/expiring-batches?cutoffAt=2026-10-13T12%3A00%3A00%2B03%3A00")
	if len(arrayField(t, expiring, "items")) != 1 {
		t.Fatalf("expiring batches = %#v", expiring)
	}
	history := getJSON(t, public, "/history")
	events := arrayField(t, history, "events")
	if len(events) != 4 {
		t.Fatalf("history = %#v", history)
	}
	historyEvent := objectField(t, events[1], "event")
	payload := objectField(t, historyEvent["payload"], "payload")
	historyDish := objectField(t, payload["dish"], "recorded dish")
	historyDishIngredient := objectField(t, arrayField(t, historyDish, "ingredients")[0], "recorded ingredient")
	if _, present := historyDishIngredient["baseUnit"]; present {
		t.Fatalf("history enriched recorded ingredient with baseUnit: %#v", historyDishIngredient)
	}
	deliveryEvent := objectField(t, events[2], "delivery event")
	deliveryPayload := objectField(t, deliveryEvent["payload"], "delivery payload")
	if objectField(t, deliveryPayload["delivery"], "delivery")["createdAt"] != "2026-10-11T11:59:59.123456789+03:00" {
		t.Fatalf("delivery timestamp lost fractional precision: %#v", deliveryPayload)
	}
	for _, route := range []struct {
		handler http.Handler
		path    string
	}{{public, "/internal/v1/products"}, {internal, "/inventory"}} {
		response := httptest.NewRecorder()
		route.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route.path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("route %q leaked across listener: status %d", route.path, response.Code)
		}
	}
}

func TestWarehouseHTTPStrictBodyAndQueryRejectionsDoNotMutate(t *testing.T) {
	runtime := newRuntime(t)
	public, internal := newHandlersWithLimit(t, runtime, 64)
	for _, body := range []string{
		`{"commandID":"c1","commandID":"c2"}`,
		`{"commandID":null,"deliveryID":"d1","number":"D1","createdAt":"2026-10-11T12:00:00+03:00","lines":[]}`,
		`{"commandID":"c1","deliveryID":"d1","number":"D1","createdAt":"2026-10-11T12:00:00+03:00","lines":[],"extra":true}`,
		`{"commandID":"c1","deliveryID":"d1","number":"D1","createdAt":"2026-10-11T12:00:00+03:00","lines":[{"productID":"p1","manufacturedAt":"2026-10-10T12:00:00+03:00","shelfLife":{"value":"1","unit":"day"},"receivedAt":"2026-10-11T12:00:00+03:00","quantity":"9223372036854775808"}]}`,
		`{"commandID":"c1","deliveryID":"d1","number":"D1","createdAt":"2026-10-11T12:00:00+03:00","lines":[]} {}`,
		`{"commandID":"c1","deliveryID":"d1","number":"D1","createdAt":"2026-10-11T12:00:00,1+03:00","lines":[]}`,
	} {
		postJSON(t, public, "/deliveries", body, http.StatusBadRequest)
	}
	for _, path := range []string{"/expiring-batches", "/expiring-batches?cutoffAt=x", "/expiring-batches?cutoffAt=2026-10-11T12%3A00%3A00%2B03%3A00&cutoffAt=2026-10-11T12%3A00%3A00%2B03%3A00", "/expiring-batches?cutoffAt=2026-10-11T12%3A00%3A00%2B03%3A00&extra=x"} {
		response := httptest.NewRecorder()
		public.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want 400", path, response.Code)
		}
	}
	if got := getJSON(t, internal, "/internal/v1/order-attempts?commandID=c1&proposalID=p1"); got["state"] != "Unseen" {
		t.Fatalf("invalid requests reserved order attempt: %#v", got)
	}
	if got := getJSON(t, public, "/history"); len(arrayField(t, got, "events")) != 0 {
		t.Fatalf("invalid requests wrote events: %#v", got)
	}
	oversized := httptest.NewRequest(http.MethodPost, "/deliveries", strings.NewReader(strings.Repeat(" ", 65)))
	oversized.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	public.ServeHTTP(response, oversized)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized request status = %d, want 400", response.Code)
	}
}

type pendingRuntime struct{ httpapi.Runtime }

func (pendingRuntime) CommitAcceptedOrder(context.Context, contract.CommitOrderRequest) (contract.CommitResult, error) {
	return contract.CommitResult{Kind: contract.CommitResultPending}, nil
}

func TestOrderCommitPendingUsesHTTP202AndTaggedPayloadOnly(t *testing.T) {
	_, internal, err := httpapi.NewHandlers(pendingRuntime{}, httpapi.Options{RequestTimeout: time.Second, MaxRequestBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/order-commits", strings.NewReader(`{"commandID":"c1","proposalID":"p1","acceptedPositions":[{"lineNo":"1","dishID":"d1","portions":"1"}]}`))
	request.Header.Set("Content-Type", "application/json")
	internal.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Body.String() != "{\"kind\":\"Pending\"}\n" {
		t.Fatalf("pending response = %d %s", response.Code, response.Body.String())
	}
}

func newRuntime(t *testing.T) *bootstrap.Runtime {
	t.Helper()
	id := 0
	runtime, err := bootstrap.NewInMemory(bootstrap.Options{Now: func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60)) }, NewID: func() string { id++; return fmt.Sprintf("batch-%d", id) }, EventsPerSnapshot: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func newHandlers(t *testing.T, runtime *bootstrap.Runtime) (http.Handler, http.Handler) {
	return newHandlersWithLimit(t, runtime, 1<<20)
}

func newHandlersWithLimit(t *testing.T, runtime *bootstrap.Runtime, maxRequestBytes int64) (http.Handler, http.Handler) {
	t.Helper()
	public, internal, err := bootstrap.NewHTTPHandlers(runtime, bootstrap.HTTPOptions{RequestTimeout: time.Second, MaxRequestBytes: maxRequestBytes})
	if err != nil {
		t.Fatal(err)
	}
	return public, internal
}

func getJSON(t *testing.T, handler http.Handler, path string) map[string]any {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s body %q: %v", path, response.Body.String(), err)
	}
	return body
}

func arrayField(t *testing.T, object map[string]any, name string) []any {
	t.Helper()
	value, ok := object[name].([]any)
	if !ok {
		t.Fatalf("field %q has type %T, want []", name, object[name])
	}
	if value == nil {
		t.Fatalf("field %q is null, want []", name)
	}
	return value
}

func objectField(t *testing.T, value any, name string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s has type %T, want object", name, value)
	}
	return object
}

func postJSON(t *testing.T, handler http.Handler, path, body string, wantStatus int) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("POST %s = %d %s, want %d", path, response.Code, response.Body.String(), wantStatus)
	}
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("POST %s body %q: %v", path, response.Body.String(), err)
	}
	return value
}
