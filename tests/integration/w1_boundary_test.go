package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/application"
)

const (
	tenantA = "33333333-3333-4333-8333-333333333333"
	tenantB = "33333333-3333-4333-8333-333333333334"
	orgID   = "22222222-2222-4222-8222-222222222222"
	epoch   = "44444444-4444-4444-8444-444444444444"
)

var now = time.Date(2026, 10, 13, 12, 0, 0, 0, time.FixedZone("MSK", 10800))

type keys struct{ key ed25519.PublicKey }

func (k keys) Key(_ context.Context, kid string) (ed25519.PublicKey, error) {
	if kid != "test-key" {
		return nil, fmt.Errorf("unknown key")
	}
	return append(ed25519.PublicKey(nil), k.key...), nil
}

type fixture struct {
	registry *bootstrap.Registry
	handler  http.Handler
	private  ed25519.PrivateKey
}

func scope(tenant string) contract.Scope {
	return contract.Scope{OrganizationID: orgID, RestaurantID: tenant, RuntimeEpoch: epoch, LifecycleVersion: "1", TimeZone: "Europe/Moscow", SettingsVersion: "1"}
}
func setup(t *testing.T) fixture { return setupWith(t, nil) }

// setupWith builds the fixture with wrap around each restaurant's event store.
func setupWith(t *testing.T, wrap func(application.EventStore) application.EventStore) fixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := bootstrap.NewRegistry(bootstrap.Options{Now: func() time.Time { return now }, NewID: rand.Text, EventsPerSnapshot: 1, WrapEventStore: wrap}, epoch)
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{tenantA, tenantB} {
		if err := reg.Register(context.Background(), scope(tenant)); err != nil {
			t.Fatal(err)
		}
	}
	v, err := bootstrap.NewJWTVerifier(bootstrap.JWTOptions{Now: func() time.Time { return now }, Keys: keys{pub}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := bootstrap.NewScopedHTTPHandler(reg, bootstrap.ScopedHTTPOptions{RequestTimeout: time.Second, MaxRequestBytes: 1 << 20, Verifier: v, CallerKeys: map[string]string{"umir-gateway": "gateway-key", "umir-serving": "serving-key", "umir-auth": "auth-key"}})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{reg, h, priv}
}
func claims(tenant, op string, permissions ...string) map[string]any {
	return map[string]any{"iss": "urn:umir:auth", "aud": []string{"umir-warehouse"}, "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Unix() + 60, "jti": "55555555-5555-4555-8555-555555555555", "kind": "user", "sub": "11111111-1111-4111-8111-111111111111", "sid": "66666666-6666-4666-8666-666666666666", "session_version": "1", "credential_version": "1", "access_version": "1", "org_id": orgID, "tenant_id": tenant, "time_zone": "Europe/Moscow", "settings_version": "1", "lifecycle_version": "1", "operation": op, "permissions": permissions, "azp": "umir-gateway"}
}
func (f fixture) token(t *testing.T, c, h map[string]any) string {
	t.Helper()
	if h == nil {
		h = map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": "test-key"}
	}
	a, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	p := base64.RawURLEncoding.EncodeToString(a) + "." + base64.RawURLEncoding.EncodeToString(b)
	return p + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.private, []byte(p)))
}
func (f fixture) call(method, tenant, path, body, token, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/internal/warehouse/v2/r/"+tenant+"/"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Umir-Runtime-Epoch", epoch)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("X-Umir-Service-Key", key)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}
func TestW1RestaurantIsolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for i, tenant := range []string{tenantA, tenantB} {
		w := f.call("POST", tenant, "products", fmt.Sprintf(`{"commandID":"same","productID":"P1","name":"name-%d","baseUnit":"g"}`, i), f.token(t, claims(tenant, "registerProduct", "catalog.write"), nil), "gateway-key")
		if w.Code != 200 {
			t.Fatalf("register: %d %s", w.Code, w.Body)
		}
	}
	for i, tenant := range []string{tenantA, tenantB} {
		r, err := f.registry.Resolve(ctx, scope(tenant))
		if err != nil {
			t.Fatal(err)
		}
		p, err := r.GetProducts(ctx)
		if err != nil || len(p.Items) != 1 || p.Items[0].Name != fmt.Sprintf("name-%d", i) || p.StateVersion != 1 {
			t.Fatalf("products %+v %v", p, err)
		}
		p.Items[0].Name = "corrupt"
		record, found, err := r.FindRecordedCommand(ctx, "same")
		if err != nil || !found || record.Result.Product.Name != fmt.Sprintf("name-%d", i) {
			t.Fatalf("record %+v %v", record, err)
		}
		record.Result.Product.Name = "corrupt"
		history, err := r.GetHistory(ctx)
		if err != nil || len(history.Events) != 1 {
			t.Fatalf("journal %+v %v", history, err)
		}
		if _, err := r.RunSnapshotOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if err := r.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		p, err = r.GetProducts(ctx)
		if err != nil || p.Items[0].Name != fmt.Sprintf("name-%d", i) {
			t.Fatalf("snapshot/copy %+v %v", p, err)
		}
	}
}
func TestW1InvalidTrustCannotWrite(t *testing.T) {
	cases := []struct {
		name         string
		mutate       func(map[string]any)
		header       map[string]any
		key          string
		badSignature bool
		want         int
	}{
		{name: "signature", badSignature: true, want: 401}, {name: "algorithm", header: map[string]any{"alg": "HS256", "typ": "JWT", "kid": "test-key"}, want: 401},
		{name: "issuer", mutate: func(c map[string]any) { c["iss"] = "forged" }, want: 401}, {name: "audience", mutate: func(c map[string]any) { c["aud"] = []string{"umir-serving"} }, want: 401},
		{name: "subject", mutate: func(c map[string]any) { c["sub"] = "service:gateway" }, want: 401}, {name: "expiry", mutate: func(c map[string]any) { c["iat"] = now.Unix() - 60; c["nbf"] = now.Unix() - 60; c["exp"] = now.Unix() }, want: 401},
		{name: "future", mutate: func(c map[string]any) {
			c["iat"] = now.Unix() + 1
			c["nbf"] = now.Unix() + 1
			c["exp"] = now.Unix() + 61
		}, want: 401},
		{name: "missing tenant", mutate: func(c map[string]any) { delete(c, "tenant_id") }, want: 401}, {name: "tenant mismatch", mutate: func(c map[string]any) { c["tenant_id"] = tenantB }, want: 403},
		{name: "org mismatch", mutate: func(c map[string]any) { c["org_id"] = "22222222-2222-4222-8222-222222222223" }, want: 403},
		{name: "permission", mutate: func(c map[string]any) { c["permissions"] = []string{"menu.read"} }, want: 403}, {name: "operation", mutate: func(c map[string]any) { c["operation"] = "inventory" }, want: 403},
		{name: "caller", key: "serving-key", want: 403}, {name: "transport", key: "bad-key", want: 401},
		{name: "unknown claim", mutate: func(c map[string]any) { c["role"] = "Admin" }, want: 401}, {name: "embedded key", header: map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": "test-key", "jku": "http://evil"}, want: 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			c := claims(tenantA, "registerProduct", "catalog.write")
			if tc.mutate != nil {
				tc.mutate(c)
			}
			token := f.token(t, c, tc.header)
			if tc.badSignature {
				token = token[:len(token)-8] + "AAAAAAAA"
			}
			key := tc.key
			if key == "" {
				key = "gateway-key"
			}
			w := f.call("POST", tenantA, "products", `{"commandID":"x","productID":"P1","name":"milk","baseUnit":"g"}`, token, key)
			if w.Code != tc.want {
				t.Fatalf("%d want %d: %s", w.Code, tc.want, w.Body)
			}
			r, err := f.registry.Resolve(context.Background(), scope(tenantA))
			if err != nil {
				t.Fatal(err)
			}
			h, err := r.GetHistory(context.Background())
			if err != nil || len(h.Events) != 0 {
				t.Fatalf("denied write %+v %v", h, err)
			}
		})
	}
}
func TestW1AllRoutesRequireTrust(t *testing.T) {
	f := setup(t)
	routes := []struct{ method, path string }{{"GET", "products"}, {"POST", "products"}, {"GET", "dish-catalog"}, {"GET", "dish"}, {"POST", "dishes"}, {"POST", "deliveries"}, {"GET", "inventory"}, {"GET", "expiring-batches"}, {"GET", "history"}, {"POST", "writeoffs"}, {"GET", "id-suggestions"}, {"GET", "analytics/stock"}, {"GET", "analytics/movements"}, {"POST", "expiry-runs"}, {"GET", "menu-evaluation"}, {"POST", "order-evaluations"}, {"POST", "order-commits"}, {"GET", "commands"}, {"GET", "orders"}, {"GET", "order-attempts"}, {"GET", "pending-writeoffs"}, {"GET", "writeoffs/commands"}, {"PUT", "registry"}, {"DELETE", "registry"}}
	for _, route := range routes {
		t.Run(route.method+route.path, func(t *testing.T) {
			w := f.call(route.method, tenantA, route.path, "", "", "gateway-key")
			if w.Code != 401 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		})
	}
	for _, path := range []string{"/inventory", "/internal/v1/products", "/api/warehouse/inventory"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("X-Umir-Tenant-ID", tenantA)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("unscoped %s %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatalf("health %d", w.Code)
	}
}
func TestW1PassiveLookupBoundToRestaurant(t *testing.T) {
	f := setup(t)
	r, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "secret", ProductID: "P1", Name: "secret", BaseUnit: contract.BaseUnitG})
	if err != nil {
		t.Fatal(err)
	}
	c := claims(tenantB, "resolveOrder", "order.read", "consumer.order.resolve")
	c["aud"] = []string{"umir-serving", "umir-warehouse"}
	w := f.call("GET", tenantB, "commands?commandID=secret", "", f.token(t, c, nil), "serving-key")
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret") || !strings.Contains(w.Body.String(), `"found":false`) {
		t.Fatalf("leak %d %s", w.Code, w.Body)
	}
	w = f.call("GET", tenantA, "commands?commandID=secret", "", f.token(t, c, nil), "serving-key")
	if w.Code != 403 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("foreign %d %s", w.Code, w.Body)
	}
}
func TestW1ConcurrentRegistrationKeepsState(t *testing.T) {
	f := setup(t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := f.registry.Register(context.Background(), scope(tenantA)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	r, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "c", ProductID: "P1", Name: "one", BaseUnit: contract.BaseUnitG})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.registry.Register(context.Background(), scope(tenantA)); err != nil {
		t.Fatal(err)
	}
	next, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	p, err := next.GetProducts(context.Background())
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("state reset %+v %v", p, err)
	}
}
func TestW1AbsentRestaurantNeverCreatesState(t *testing.T) {
	f := setup(t)
	unknown := "33333333-3333-4333-8333-333333333335"
	for _, tc := range []struct{ method, path, op, permission, body string }{{"GET", "products", "products", "catalog.read", ""}, {"POST", "products", "registerProduct", "catalog.write", `{"commandID":"c","productID":"P1","name":"milk","baseUnit":"g"}`}, {"GET", "commands?commandID=c", "resolveOrder", "consumer.order.resolve", ""}} {
		c := claims(unknown, tc.op, tc.permission)
		key := "gateway-key"
		if tc.op == "resolveOrder" {
			key = "serving-key"
			c["aud"] = []string{"umir-serving", "umir-warehouse"}
		}
		w := f.call(tc.method, unknown, tc.path, tc.body, f.token(t, c, nil), key)
		if w.Code != 503 || !strings.Contains(w.Body.String(), "RuntimeNotReady") {
			t.Fatalf("unregistered %d %s", w.Code, w.Body)
		}
	}
	if _, err := f.registry.Resolve(context.Background(), scope(unknown)); err == nil {
		t.Fatal("request created runtime")
	}
}
func TestW1ForgedBrowserHeadersAndSavedEpoch(t *testing.T) {
	f := setup(t)
	token := f.token(t, claims(tenantA, "products", "catalog.read"), nil)
	for _, header := range []string{"X-Umir-Tenant-ID", "X-Umir-User-ID", "X-Umir-Org-ID", "X-Umir-Roles", "X-Umir-Caller", "Cookie"} {
		r := httptest.NewRequest("GET", "/internal/warehouse/v2/r/"+tenantA+"/products", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Umir-Service-Key", "gateway-key")
		r.Header.Set(header, "forged")
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s %d %s", header, w.Code, w.Body)
		}
	}
	c := claims(tenantA, "resolveOrder", "consumer.order.resolve")
	c["aud"] = []string{"umir-serving", "umir-warehouse"}
	r := httptest.NewRequest("GET", "/internal/warehouse/v2/r/"+tenantA+"/commands?commandID=c", nil)
	r.Header.Set("Authorization", "Bearer "+f.token(t, c, nil))
	r.Header.Set("X-Umir-Service-Key", "serving-key")
	r.Header.Set("X-Umir-Runtime-Epoch", "44444444-4444-4444-8444-444444444445")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"outcome":"indeterminate"`) {
		t.Fatalf("epoch %d %s", w.Code, w.Body)
	}
}
func TestW1ServiceCannotBecomeUser(t *testing.T) {
	f := setup(t)
	c := map[string]any{"iss": "urn:umir:auth", "aud": []string{"umir-warehouse"}, "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Unix() + 60, "jti": "55555555-5555-4555-8555-555555555555", "kind": "service", "sub": "service:gateway", "org_id": orgID, "tenant_id": tenantA, "runtime_epoch": epoch, "lifecycle_version": "1", "operation": "expiry.due", "permissions": []string{"expiry.due"}, "azp": "umir-gateway"}
	w := f.call("POST", tenantA, "writeoffs", `{}`, f.token(t, c, nil), "gateway-key")
	if w.Code != 403 {
		t.Fatalf("service writeoff %d %s", w.Code, w.Body)
	}
	c["sid"] = ""
	w = f.call("POST", tenantA, "expiry-runs", `{"runID":"77777777-7777-4777-8777-777777777777"}`, f.token(t, c, nil), "gateway-key")
	if w.Code != 401 {
		t.Fatalf("service accepted user claim %d %s", w.Code, w.Body)
	}
}
func TestW1RegistryWorkerCancellation(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.registry.RunSnapshotWorker(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("worker cancellation %v", err)
	}
}

func TestW1TokenZoneAndDiscoveryFailures(t *testing.T) {
	f := setup(t)
	t.Run("zone", func(t *testing.T) {
		c := claims(tenantA, "registerProduct", "catalog.write")
		c["time_zone"] = "Local"
		w := f.call("POST", tenantA, "products", `{"commandID":"zone","productID":"P1","name":"milk","baseUnit":"g"}`, f.token(t, c, nil), "gateway-key")
		if w.Code != 401 {
			t.Fatalf("host-local zone trusted: %d %s", w.Code, w.Body)
		}
	})
	t.Run("discovery", func(t *testing.T) {
		w := f.call("GET", tenantA, "products", "", f.token(t, claims(tenantA, "products", "catalog.read"), map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": "unavailable"}), "gateway-key")
		if w.Code != 503 || !strings.Contains(w.Body.String(), "AuthUnavailable") {
			t.Fatalf("key discovery %d %s", w.Code, w.Body)
		}
	})
}
func TestW1ServingCatalogOperationCannotChangeRoute(t *testing.T) {
	f := setup(t)
	c := claims(tenantA, "servingProducts", "catalog.read")
	c["aud"] = []string{"umir-serving", "umir-warehouse"}
	w := f.call("GET", tenantA, "dish-catalog", "", f.token(t, c, nil), "serving-key")
	if w.Code != 403 {
		t.Fatalf("forwarded product authorization used for dishes: %d %s", w.Code, w.Body)
	}
}

func TestW1ValidSchedulerHasOnlyDueAuthority(t *testing.T) {
	f := setup(t)
	c := map[string]any{"iss": "urn:umir:auth", "aud": []string{"umir-warehouse"}, "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Unix() + 60, "jti": "55555555-5555-4555-8555-555555555555", "kind": "service", "sub": "service:gateway", "org_id": orgID, "tenant_id": tenantA, "runtime_epoch": epoch, "lifecycle_version": "1", "operation": "expiry.due", "permissions": []string{"expiry.due"}, "azp": "umir-gateway"}
	w := f.call("POST", tenantA, "expiry-runs", `{"runID":"77777777-7777-4777-8777-777777777777"}`, f.token(t, c, nil), "gateway-key")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"finalWriteoffs":0`) {
		t.Fatalf("valid due actor was rejected as invalid scope: %d %s", w.Code, w.Body)
	}
	w = f.call("GET", tenantA, "inventory", "", f.token(t, c, nil), "gateway-key")
	if w.Code != 403 {
		t.Fatalf("scheduler read inventory: %d %s", w.Code, w.Body)
	}
}
func TestW1ActiveRegistryWorkersStopTogether(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.registry.RunSnapshotWorker(ctx) }()
	tenant := "33333333-3333-4333-8333-333333333335"
	if err := f.registry.Register(ctx, scope(tenant)); err != nil {
		t.Fatal(err)
	}
	r, err := f.registry.Resolve(ctx, scope(tenant))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "c", ProductID: "P1", Name: "one", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("workers stopped with %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("workers did not stop")
	}
}

func TestW1DishResponseIncludesIngredientUnits(t *testing.T) {
	f := setup(t)
	r, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "product", ProductID: "P1", Name: "milk", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	w := f.call("POST", tenantA, "dishes", `{"commandID":"dish","dishID":"D1","name":"dish","ingredients":[{"productID":"P1","quantity":"9007199254740993"}]}`, f.token(t, claims(tenantA, "defineDish", "catalog.write"), nil), "gateway-key")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"baseUnit":"g"`) || !strings.Contains(w.Body.String(), `"quantity":"9007199254740993"`) {
		t.Fatalf("dish wire %d %s", w.Code, w.Body)
	}
}
func TestW1StockAndJournalNeverEmitLegacyShape(t *testing.T) {
	f := setup(t)
	for _, path := range []string{"inventory", "history"} {
		w := f.call("GET", tenantA, path, "", f.token(t, claims(tenantA, path, "stock.read"), nil), "gateway-key")
		if w.Code == 503 && strings.Contains(w.Body.String(), "ServiceUnavailable") {
			continue
		}
		if w.Code != 200 {
			t.Fatalf("v2 %s: %d %s", path, w.Code, w.Body)
		}
		var response struct {
			Result map[string]json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		required := "items"
		if path == "history" {
			required = "events"
		}
		if len(response.Result) != 1 || response.Result[required] == nil {
			t.Fatalf("v2 %s emitted legacy shape %s", path, w.Body)
		}
	}
}
