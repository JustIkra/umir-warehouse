package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/tests/fixtures/protocol"
)

func testConfig() ownerConfig {
	ref := func(name string) fileRef { return fileRef{"warehouse", secretRoot + name} }
	return ownerConfig{Version: 1, Protocol: protocolVersion, Project: "umir-backend-" + strings.Repeat("a", 24), Owner: "warehouse", FixtureID: "22222222-2222-4222-8222-222222222222", InitialInstant: "2026-10-13T09:00:00+03:00", ClockGeneration: "1", ControlCredentialFile: ref("fixture-warehouse"), CredentialFiles: map[string]fileRef{"auth-warehouse": ref("auth-warehouse"), "gateway-warehouse": ref("gateway-warehouse"), "serving-warehouse": ref("serving-warehouse")}, ControlSocket: socketPath, PrivatePort: 8080, Origin: "http://127.0.0.1:12345", ServiceURLs: serviceURLs{"http://auth:8080", "http://gateway:8082", "http://warehouse:8080", "http://serving:8080", "http://feedback:8080"}, Delivery: deliveryConfig{"smtp", 2525, "starttls", fileRef{"auth", secretRoot + "synthetic-ca.pem"}, "http://notification:3128", fileRef{"feedback", secretRoot + "synthetic-ca.pem"}}, Bootstrap: json.RawMessage("null")}
}
func TestW6ConfigRejectsWrongBindings(t *testing.T) {
	cfg := testConfig()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, digest, err := parseConfig(raw)
	if err != nil || !validDigest(digest) {
		t.Fatal(digest, err)
	}
	t.Logf("WIRE_SCHEMA OwnerConfig %s", raw)
	for _, mutate := range []func(*ownerConfig){func(c *ownerConfig) { c.Owner = "serving" }, func(c *ownerConfig) { c.CredentialFiles["fixture-warehouse"] = c.ControlCredentialFile }, func(c *ownerConfig) {
		c.CredentialFiles["auth-warehouse"] = fileRef{"auth", secretRoot + "auth-warehouse"}
	}, func(c *ownerConfig) { c.ServiceURLs.Auth = "http://evil.invalid:8080" }, func(c *ownerConfig) { c.StateFile = "/var/lib/umir-auth/auth.json" }, func(c *ownerConfig) { c.ClockGeneration = "01" }, func(c *ownerConfig) { c.InitialInstant = "2026-10-13T09:00:00+24:00" }, func(c *ownerConfig) { c.Bootstrap = json.RawMessage(`{}`) }} {
		c := testConfig()
		mutate(&c)
		b, e := json.Marshal(c)
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = parseConfig(b); e == nil {
			t.Fatal("invalid binding accepted")
		}
	}
	for _, bad := range []string{strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1), string(raw) + "{}", strings.Replace(string(raw), `"version":1`, `"extra":true,"version":1`, 1)} {
		if _, _, err := parseConfig([]byte(bad)); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
}

type composeFixture struct {
	app     *process
	c       *control
	private ed25519.PrivateKey
	scope   contract.Scope
	keys    serviceKeys
}

func testComposition(t *testing.T) composeFixture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := serviceKeys{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/auth/v1/jwks" || r.Header.Get("X-Umir-Service-Key") != keys.Lifecycle {
			w.WriteHeader(401)
			return
		}
		b, e := json.Marshal(map[string]any{"keys": []any{map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": "unit-only-key", "x": base64.RawURLEncoding.EncodeToString(public), "alg": "EdDSA", "use": "sig"}}})
		if e != nil {
			t.Error(e)
			return
		}
		if _, e = w.Write(b); e != nil {
			t.Error(e)
		}
	}))
	t.Cleanup(auth.Close)
	cfg := testConfig()
	cfg.ServiceURLs.Auth = auth.URL
	c, err := newControl(cfg.Project, "11111111-1111-4111-8111-111111111111", cfg.FixtureID, cfg.InitialInstant, cfg.ClockGeneration, strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64), func(context.Context) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	epoch := "44444444-4444-4444-8444-444444444444"
	app, err := compose(cfg, keys, c.now, epoch, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.CloseIdleConnections)
	c.faults = app.faults
	c.ready = func(ctx context.Context) bool { return ownerReady(ctx, app, keys.Lifecycle, epoch) }
	scope := contract.Scope{OrganizationID: "22222222-2222-4222-8222-222222222222", RestaurantID: "33333333-3333-4333-8333-333333333333", RuntimeEpoch: epoch, LifecycleVersion: "1", TimeZone: "Europe/Moscow", SettingsVersion: "1"}
	if err = app.registry.Register(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	return composeFixture{app, c, private, scope, keys}
}
func (f composeFixture) token(t *testing.T, operation, permission, jti string) string {
	t.Helper()
	claims := map[string]any{"iss": "urn:umir:auth", "aud": []string{"umir-warehouse"}, "iat": f.c.now().Unix(), "nbf": f.c.now().Unix(), "exp": f.c.now().Unix() + 60, "jti": jti, "kind": "user", "sub": "11111111-1111-4111-8111-111111111111", "sid": "66666666-6666-4666-8666-666666666666", "session_version": "1", "credential_version": "1", "access_version": "1", "org_id": f.scope.OrganizationID, "tenant_id": f.scope.RestaurantID, "time_zone": f.scope.TimeZone, "settings_version": "1", "lifecycle_version": "1", "operation": operation, "permissions": []string{permission}, "azp": "umir-gateway"}
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"JWT","kid":"unit-only-key"}`))
	unsigned := h + "." + base64.RawURLEncoding.EncodeToString(raw)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.private, []byte(unsigned)))
}
func (f composeFixture) call(t *testing.T, method, path, body, operation, permission, jti string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/internal/warehouse/v2/r/"+f.scope.RestaurantID+"/"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Umir-Service-Key", f.keys.Gateway)
	r.Header.Set("X-Umir-Runtime-Epoch", f.scope.RuntimeEpoch)
	r.Header.Set("Authorization", "Bearer "+f.token(t, operation, permission, jti))
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	return w
}
func TestW6CompositionRealJWTClockAndMemoryReset(t *testing.T) {
	f := testComposition(t)
	ctx := context.Background()
	if !ownerReady(ctx, f.app, f.keys.Lifecycle, f.scope.RuntimeEpoch) {
		t.Fatal("real composition not ready")
	}
	jti := "55555555-5555-4555-8555-555555555555"
	w := f.call(t, "POST", "products", `{"commandID":"p","productID":"P","name":"Product","baseUnit":"g"}`, "registerProduct", "catalog.write", jti)
	if w.Code != 200 {
		t.Fatalf("public consumer failed %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("GET", "/internal/warehouse/v2/r/"+f.scope.RestaurantID+"/products", nil)
	r.Header.Set("X-Umir-Service-Key", f.keys.Gateway)
	r.Header.Set("Authorization", "Bearer "+f.token(t, "products", "catalog.read", jti))
	f.c.clockMu.Lock()
	f.c.instant = f.c.instant.Add(60 * time.Second)
	f.c.clockMu.Unlock()
	expired := httptest.NewRecorder()
	f.app.ServeHTTP(expired, r)
	if expired.Code != 401 {
		t.Fatal("JWT used host clock", expired.Code)
	}
	newer, err := compose(testConfig(), f.keys, f.c.now, "99999999-9999-4999-8999-999999999999", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer newer.CloseIdleConnections()
	if _, err = newer.registry.Resolve(ctx, f.scope); !errors.Is(err, contract.ErrRuntimeReset) {
		t.Fatal("restart reused epoch", err)
	}
	next := f.scope
	next.RuntimeEpoch = newer.registry.Epoch()
	if err = newer.registry.Register(ctx, next); err != nil {
		t.Fatal(err)
	}
	runtime, err := newer.registry.Resolve(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	products, err := runtime.GetProducts(ctx)
	if err != nil || len(products.Items) != 0 {
		t.Fatal("memory reseeded", products, err)
	}
}
func TestW6HTTPPreparationFaultKeepsInvalidation(t *testing.T) {
	f := testComposition(t)
	ctx := context.Background()
	r, err := f.app.registry.Resolve(ctx, f.scope)
	if err != nil {
		t.Fatal(err)
	}
	at := f.c.now()
	if _, err = r.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p", ProductID: "P", Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "d", DeliveryID: "d", Number: "1", CreatedAt: at, Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 17, ManufacturedAt: at, ReceivedAt: at, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}}); err != nil {
		t.Fatal(err)
	}
	f.c.clockMu.Lock()
	f.c.instant = f.c.instant.Add(24 * time.Hour)
	f.c.clockMu.Unlock()
	jti := "55555555-5555-4555-8555-555555555555"
	id := "77777777-7777-4777-8777-777777777777"
	if _, err = f.app.faults.Arm(protocol.FaultArm{FaultID: id, Point: protocol.AnalyticsPoint, OperationID: "analyticsstock", CommandID: jti, ExpireAfterMs: 30000, Count: 1}); err != nil {
		t.Fatal(err)
	}
	w := f.call(t, "GET", "analytics/stock?expectedSettingsVersion=1", "", "analyticsstock", "analytics.read", jti)
	if w.Code != 503 || w.Header().Get("X-Umir-Committed-Version") != "3" || w.Header().Get("X-Umir-Invalidation-Required") != "true" {
		t.Fatalf("committed preparation hidden %d %+v %s", w.Code, w.Header(), w.Body)
	}
	v, err := f.app.faults.Read(id)
	if err != nil || v.OwnerOutcome != "committed" || v.Hits != 1 {
		t.Fatal(v, err)
	}
	w = f.call(t, "GET", "analytics/stock?expectedSettingsVersion=1", "", "analyticsstock", "analytics.read", jti)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestW6ActualHTTPReplyLossPreservesCommit(t *testing.T) {
	f := testComposition(t)
	id := "77777777-7777-4777-8777-777777777777"
	if _, err := f.app.faults.Arm(protocol.FaultArm{FaultID: id, Point: "after-durable-before-reply", OperationID: "registerProduct", CommandID: "lost", ExpireAfterMs: 30000, Count: 1}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.app)
	defer server.Close()
	r, err := http.NewRequest("POST", server.URL+"/internal/warehouse/v2/r/"+f.scope.RestaurantID+"/products", strings.NewReader(`{"commandID":"lost","productID":"P","name":"P","baseUnit":"g"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.token(t, "registerProduct", "catalog.write", "55555555-5555-4555-8555-555555555555"))
	r.Header.Set("X-Umir-Service-Key", f.keys.Gateway)
	r.Header.Set("X-Umir-Runtime-Epoch", f.scope.RuntimeEpoch)
	client := &http.Client{Timeout: time.Second}
	reply, err := client.Do(r)
	if err == nil {
		if _, err = io.Copy(io.Discard, reply.Body); err != nil {
			t.Fatal(err)
		}
		if err = reply.Body.Close(); err != nil {
			t.Fatal(err)
		}
		t.Fatal("reply not lost")
	}
	runtime, err := f.app.registry.Resolve(context.Background(), f.scope)
	if err != nil {
		t.Fatal(err)
	}
	_, found, err := runtime.FindRecordedCommand(context.Background(), "lost")
	if err != nil || !found {
		t.Fatal("real commit missing", err)
	}
}
