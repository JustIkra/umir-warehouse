package integration_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/httpapi"
)

type routeInputDirectory struct {
	registry                                *bootstrap.Registry
	epochCalls, resolveCalls, dispatchCalls int
}

func (d *routeInputDirectory) Epoch() string {
	d.epochCalls++
	return epoch
}

func (d *routeInputDirectory) RuntimeFor(ctx context.Context, s contract.Scope) (httpapi.Runtime, error) {
	d.resolveCalls++
	runtime, err := d.registry.RuntimeFor(ctx, s)
	if err != nil {
		return nil, err
	}
	return routeInputRuntime{runtime, &d.dispatchCalls}, nil
}

type routeInputRuntime struct {
	httpapi.Runtime
	calls *int
}

func (r routeInputRuntime) GetProducts(ctx context.Context) (contract.Products, error) {
	*r.calls++
	return r.Runtime.GetProducts(ctx)
}

func (r routeInputRuntime) RegisterProduct(ctx context.Context, request contract.RegisterProductRequest) (contract.Product, error) {
	*r.calls++
	return r.Runtime.RegisterProduct(ctx, request)
}

func (r routeInputRuntime) EvaluateOrder(ctx context.Context, request contract.EvaluateOrderRequest) (contract.OrderEvaluation, error) {
	*r.calls++
	return r.Runtime.EvaluateOrder(ctx, request)
}

type routeInputVerifier struct {
	httpapi.Verifier
	calls int
}

func (v *routeInputVerifier) Validate(ctx context.Context, token, caller, tenant, operation, expectedEpoch string) (contract.Identity, error) {
	v.calls++
	return v.Verifier.Validate(ctx, token, caller, tenant, operation, expectedEpoch)
}

func routeInputHandler(t *testing.T, f fixture) (http.Handler, *routeInputDirectory, *routeInputVerifier) {
	t.Helper()
	public, ok := f.private.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("fixture signing key is not Ed25519")
	}
	verifier, err := bootstrap.NewJWTVerifier(bootstrap.JWTOptions{Now: func() time.Time { return now }, Keys: keys{public}})
	if err != nil {
		t.Fatal(err)
	}
	v := &routeInputVerifier{Verifier: verifier}
	d := &routeInputDirectory{registry: f.registry}
	h, err := httpapi.NewScopedHandler(d, httpapi.ScopedOptions{RequestTimeout: time.Second, MaxRequestBytes: 1 << 20, Verifier: v, CallerKeys: map[string]string{"umir-gateway": "gateway-key", "umir-serving": "serving-key"}})
	if err != nil {
		t.Fatal(err)
	}
	d.epochCalls = 0
	return h, d, v
}

func TestW1RouteInputRefusalBeforeTrustAndRuntime(t *testing.T) {
	cases := []struct {
		name, method, tenant, path, outcome, code string
		status                                    int
	}{
		{"evaluation", "POST", "invalid", "/order-evaluations", "unavailable", "InvalidArgument", 400},
		{"product mutation", "POST", "invalid", "/products", "not_committed", "InvalidArgument", 400},
		{"order commit mutation", "POST", "invalid", "/order-commits", "not_committed", "InvalidArgument", 400},
		{"registration mutation", "PUT", "invalid", "/registry", "not_committed", "InvalidArgument", 400},
		{"close mutation", "DELETE", "invalid", "/registry", "not_committed", "InvalidArgument", 400},
		{"product read", "GET", "invalid", "/products", "unavailable", "InvalidArgument", 400},
		{"passive read", "GET", "invalid", "/commands", "unavailable", "InvalidArgument", 400},
		{"nested read", "GET", "invalid", "/analytics/stock", "unavailable", "InvalidArgument", 400},
		{"unknown POST route", "POST", "invalid", "/unknown", "not_committed", "InvalidArgument", 400},
		{"unknown GET route", "GET", "invalid", "/unknown", "unavailable", "InvalidArgument", 400},
		{"unknown method", "PATCH", "invalid", "/order-evaluations", "not_committed", "InvalidArgument", 400},
		{"unknown HEAD method", "HEAD", "invalid", "/products", "not_committed", "InvalidArgument", 400},
		{"unsupported GET evaluation", "GET", "invalid", "/order-evaluations", "unavailable", "InvalidArgument", 400},
		{"empty tenant evaluation", "POST", "", "/order-evaluations", "unavailable", "InvalidArgument", 400},
		{"missing operation", "POST", "invalid", "", "not_committed", "InvalidArgument", 400},
		{"empty operation", "POST", "invalid", "/", "not_committed", "InvalidArgument", 400},
		{"valid tenant unknown POST route", "POST", tenantA, "/unknown", "not_committed", "MethodNotAllowed", 405},
		{"valid tenant unknown GET route", "GET", tenantA, "/unknown", "unavailable", "MethodNotAllowed", 405},
		{"valid tenant unknown method", "PATCH", tenantA, "/order-evaluations", "not_committed", "MethodNotAllowed", 405},
	}
	for _, tc := range cases {
		for _, auth := range []string{"valid", "invalid signature", "invalid caller key", "missing authorization"} {
			t.Run(tc.name+"/"+auth, func(t *testing.T) {
				f := setup(t)
				h, d, v := routeInputHandler(t, f)
				payload := claims(tenantA, "registerProduct", "catalog.write")
				key := "gateway-key"
				if strings.Contains(tc.path, "order-") {
					payload = claims(tenantA, "prepareOrder", "consumer.order.evaluate")
					payload["aud"] = []string{"umir-serving", "umir-warehouse"}
					key = "serving-key"
				}
				token := f.token(t, payload, nil)
				switch auth {
				case "invalid signature":
					token = token[:len(token)-8] + "AAAAAAAA"
				case "invalid caller key":
					key = "invalid-key"
				}
				r := httptest.NewRequest(tc.method, "/internal/warehouse/v2/r/"+tc.tenant+tc.path, strings.NewReader(`{"positions":[]}`))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Umir-Service-Key", key)
				if auth != "missing authorization" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				r.Header.Set("X-Umir-Runtime-Epoch", epoch)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				var response struct {
					Code, Outcome string
					Retryable     bool
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if w.Code != tc.status || response.Code != tc.code || response.Outcome != tc.outcome || response.Retryable {
					t.Errorf("got %d %s; want %d %s/%s, non-retryable", w.Code, w.Body, tc.status, tc.code, tc.outcome)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Error("input refusal is cacheable")
				}
				if v.calls != 0 || d.epochCalls != 0 || d.resolveCalls != 0 || d.dispatchCalls != 0 {
					t.Errorf("refusal reached trust/runtime: verifier=%d epoch=%d resolve=%d dispatch=%d", v.calls, d.epochCalls, d.resolveCalls, d.dispatchCalls)
				}
			})
		}
	}
}

func TestW1RouteInputValidTenantTrustBeforeRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, path, operation, permission, key, body, outcome string
		validSignature                                        bool
		status, resolutions, dispatches                       int
	}{
		{"evaluation invalid signature", "order-evaluations", "prepareOrder", "consumer.order.evaluate", "serving-key", `{"positions":[]}`, "unavailable", false, 401, 0, 0},
		{"product invalid signature", "products", "registerProduct", "catalog.write", "gateway-key", `{"commandID":"c","productID":"P1","name":"milk","baseUnit":"g"}`, "not_committed", false, 401, 0, 0},
		{"commit invalid signature", "order-commits", "commitOrder", "consumer.order.commit", "serving-key", `{"positions":[]}`, "not_committed", false, 401, 0, 0},
		{"product accepted control", "products", "registerProduct", "catalog.write", "gateway-key", `{"commandID":"c","productID":"P1","name":"milk","baseUnit":"g"}`, "", true, 200, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			h, d, v := routeInputHandler(t, f)
			payload := claims(tenantA, tc.operation, tc.permission)
			if tc.key == "serving-key" {
				payload["aud"] = []string{"umir-serving", "umir-warehouse"}
			}
			token := f.token(t, payload, nil)
			if !tc.validSignature {
				token = token[:len(token)-8] + "AAAAAAAA"
			}
			r := httptest.NewRequest("POST", "/internal/warehouse/v2/r/"+tenantA+"/"+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Umir-Service-Key", tc.key)
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("X-Umir-Runtime-Epoch", epoch)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			var response struct{ Code, Outcome string }
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || response.Outcome != tc.outcome || (!tc.validSignature && response.Code != "InvalidToken") {
				t.Errorf("got %d %s; want %d outcome=%s", w.Code, w.Body, tc.status, tc.outcome)
			}
			if v.calls != 1 || d.epochCalls != 1 || d.resolveCalls != tc.resolutions || d.dispatchCalls != tc.dispatches {
				t.Errorf("unexpected trust/runtime calls: verifier=%d epoch=%d resolve=%d dispatch=%d", v.calls, d.epochCalls, d.resolveCalls, d.dispatchCalls)
			}
		})
	}
}
