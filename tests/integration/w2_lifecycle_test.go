package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/JustIkra/umir-warehouse/contract"
)

const registerOperation = "77777777-7777-4777-8777-777777777777"
const closeOperation = "88888888-8888-4888-8888-888888888888"

func lifecycleClaims(tenant, operation, version, runtimeEpoch string) map[string]any {
	c := claims(tenant, operation, operation)
	for _, key := range []string{"sid", "session_version", "credential_version", "access_version", "time_zone", "settings_version"} {
		delete(c, key)
	}
	c["kind"], c["sub"], c["azp"] = "service", "service:auth", "umir-auth"
	c["lifecycle_version"], c["runtime_epoch"] = version, runtimeEpoch
	return c
}
func lifecycleBody(operation, runtimeEpoch, version string, register bool) string {
	settings := ""
	if register {
		settings = `,"settings":{"timeZone":"Europe/Moscow","settingsVersion":"1"}`
	}
	return fmt.Sprintf(`{"operationID":%q,"expectedEpoch":%q,"lifecycleVersion":%q%s}`, operation, runtimeEpoch, version, settings)
}

type lifecycleResponse struct {
	code int
	body string
}

func (f fixture) lifecycle(t *testing.T, method, tenant, version string) lifecycleResponse {
	t.Helper()
	operation, id := "runtime.register", registerOperation
	if method == "DELETE" {
		operation, id = "runtime.close", closeOperation
	}
	w := f.call(method, tenant, "registry", lifecycleBody(id, epoch, version, method == "PUT"), f.token(t, lifecycleClaims(tenant, operation, version, epoch), nil), "auth-key")
	return lifecycleResponse{w.Code, w.Body.String()}
}
func requireLifecycle(t *testing.T, response lifecycleResponse, state, version string) {
	t.Helper()
	if response.code != 200 || !strings.Contains(response.body, `"state":"`+state+`"`) || !strings.Contains(response.body, `"lifecycleVersion":"`+version+`"`) || !strings.Contains(response.body, `"epoch":"`+epoch+`"`) || !strings.Contains(response.body, `"service":"warehouse"`) {
		t.Fatalf("lifecycle acknowledgement: %d %s", response.code, response.body)
	}
}
func TestW2DuplicateRegistrationRetainsRuntime(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r, err := f.registry.Resolve(ctx, scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "seed", ProductID: "P1", Name: "rice", BaseUnit: "g"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		requireLifecycle(t, f.lifecycle(t, "PUT", tenantA, "1"), "ready", "1")
	}
	again, err := f.registry.Resolve(ctx, scope(tenantA))
	if err != nil || again != r {
		t.Fatalf("runtime replaced: %v", err)
	}
	products, err := again.GetProducts(ctx)
	if err != nil || len(products.Items) != 1 || products.StateVersion != 1 {
		t.Fatalf("lost state: %+v %v", products, err)
	}
}
func TestW2CloseFencesRetainedHandles(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r, err := f.registry.Resolve(ctx, scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	requireLifecycle(t, f.lifecycle(t, "DELETE", tenantA, "2"), "closed", "2")
	if _, err := r.GetProducts(ctx); err == nil {
		t.Fatal("retained handle reads deleted restaurant")
	}
	if _, err := r.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "stale", ProductID: "P1", Name: "rice", BaseUnit: "g"}); err == nil {
		t.Fatal("retained handle mutates deleted restaurant")
	}
	if err := r.Recover(ctx); err == nil {
		t.Fatal("retained handle recovers deleted restaurant")
	}
	if _, err := f.registry.Resolve(ctx, scope(tenantA)); err == nil {
		t.Fatal("deleted restaurant resolves")
	}
	for _, v := range []string{"1", "2", "3"} {
		response := f.lifecycle(t, "PUT", tenantA, v)
		if response.code != 410 || !strings.Contains(response.body, `"code":"ScopeDeleted"`) {
			t.Fatalf("recreated deleted ID generation %s: %+v", v, response)
		}
	}
	requireLifecycle(t, f.lifecycle(t, "DELETE", tenantA, "2"), "closed", "2")
	neighbor, err := f.registry.Resolve(ctx, scope(tenantB))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := neighbor.GetProducts(ctx); err != nil {
		t.Fatalf("neighbor damaged: %v", err)
	}
}
func TestW2DeleteRacingRegistration(t *testing.T) {
	f := setup(t)
	tokenRegister := f.token(t, lifecycleClaims(tenantA, "runtime.register", "1", epoch), nil)
	tokenClose := f.token(t, lifecycleClaims(tenantA, "runtime.close", "2", epoch), nil)
	var workers sync.WaitGroup
	failures := make(chan string, 32)
	for range 16 {
		workers.Go(func() {
			w := f.call("PUT", tenantA, "registry", lifecycleBody(registerOperation, epoch, "1", true), tokenRegister, "auth-key")
			if w.Code != 200 && w.Code != 410 {
				failures <- w.Body.String()
			}
		})
		workers.Go(func() {
			w := f.call("DELETE", tenantA, "registry", lifecycleBody(closeOperation, epoch, "2", false), tokenClose, "auth-key")
			if w.Code != 200 {
				failures <- w.Body.String()
			}
		})
	}
	workers.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	if err := f.registry.Register(context.Background(), scope(tenantA)); err == nil {
		t.Fatal("stale registration resurrected deleted ID")
	}
	if _, err := f.registry.Resolve(context.Background(), scope(tenantA)); err == nil {
		t.Fatal("deleted restaurant resolves after race")
	}
}
func TestW2LifecycleEpochAndGenerationBinding(t *testing.T) {
	f := setup(t)
	oldEpoch := "44444444-4444-4444-8444-444444444445"
	w := f.call("PUT", tenantA, "registry", lifecycleBody(registerOperation, oldEpoch, "1", true), f.token(t, lifecycleClaims(tenantA, "runtime.register", "1", epoch), nil), "auth-key")
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"outcome":"indeterminate"`) {
		t.Fatalf("old epoch authorized: %d %s", w.Code, w.Body)
	}
	w = f.call("PUT", tenantA, "registry", lifecycleBody(registerOperation, epoch, "2", true), f.token(t, lifecycleClaims(tenantA, "runtime.register", "1", epoch), nil), "auth-key")
	if w.Code != 403 {
		t.Fatalf("body generation overrides signed scope: %d %s", w.Code, w.Body)
	}
	missing := scope("33333333-3333-4333-8333-333333333335")
	if _, err := f.registry.Resolve(context.Background(), missing); !errors.Is(err, contract.ErrRuntimeNotReady) {
		t.Fatalf("unknown tenant: %v", err)
	}
}

func TestW2RegistrationOperationIdentity(t *testing.T) {
	f := setup(t)
	requireLifecycle(t, f.lifecycle(t, "PUT", tenantA, "1"), "ready", "1")
	other := "77777777-7777-4777-8777-777777777778"
	w := f.call("PUT", tenantA, "registry", lifecycleBody(other, epoch, "1", true), f.token(t, lifecycleClaims(tenantA, "runtime.register", "1", epoch), nil), "auth-key")
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"CommandConflict"`) {
		t.Fatalf("changed saved registration operation accepted: %d %s", w.Code, w.Body)
	}
}

func TestW2LifecycleStrictInputsAndScope(t *testing.T) {
	f := setup(t)
	valid := lifecycleBody(registerOperation, epoch, "1", true)
	token := f.token(t, lifecycleClaims(tenantA, "runtime.register", "1", epoch), nil)
	for name, body := range map[string]string{
		"missingOperation":       strings.Replace(valid, `"operationID":"`+registerOperation+`",`, "", 1),
		"duplicate":              strings.Replace(valid, `"operationID":`, `"operationID":"`+registerOperation+`","operationID":`, 1),
		"unknown":                strings.Replace(valid, `{`, `{"extra":true,`, 1),
		"nonCanonicalGeneration": strings.Replace(valid, `"lifecycleVersion":"1"`, `"lifecycleVersion":"01"`, 1),
		"missingSettings":        lifecycleBody(registerOperation, epoch, "1", false),
		"invalidZone":            strings.Replace(valid, "Europe/Moscow", "Invalid/Zone", 1),
		"nullSettings":           strings.Replace(valid, `{"timeZone":"Europe/Moscow","settingsVersion":"1"}`, "null", 1),
		"query":                  valid,
	} {
		t.Run(name, func(t *testing.T) {
			path := "registry"
			if name == "query" {
				path += "?extra=1"
			}
			w := f.call("PUT", tenantA, path, body, token, "auth-key")
			if w.Code != 400 || !strings.Contains(w.Body.String(), `"outcome":"not_committed"`) {
				t.Fatalf("invalid input: %d %s", w.Code, w.Body)
			}
		})
	}
	other := lifecycleClaims(tenantA, "runtime.close", "2", epoch)
	other["org_id"] = "22222222-2222-4222-8222-222222222223"
	w := f.call("DELETE", tenantA, "registry", lifecycleBody(closeOperation, epoch, "2", false), f.token(t, other, nil), "auth-key")
	if w.Code != 403 {
		t.Fatalf("cross-org deletion: %d %s", w.Code, w.Body)
	}
	requireLifecycle(t, f.lifecycle(t, "DELETE", tenantA, "2"), "closed", "2")
	w = f.call("DELETE", tenantA, "registry", lifecycleBody(closeOperation, epoch, "2", false), f.token(t, other, nil), "auth-key")
	if w.Code != 403 {
		t.Fatalf("cross-org tombstone disclosure: %d %s", w.Code, w.Body)
	}
}
func TestW2UnknownBusinessRequestDoesNotRegister(t *testing.T) {
	f := setup(t)
	missing := "33333333-3333-4333-8333-333333333335"
	for _, input := range []struct{ method, path, body, operation, permission string }{
		{"GET", "products", "", "products", "catalog.read"},
		{"POST", "products", `{"commandID":"same","productID":"P1","name":"rice","baseUnit":"g"}`, "registerProduct", "catalog.write"},
	} {
		w := f.call(input.method, missing, input.path, input.body, f.token(t, claims(missing, input.operation, input.permission), nil), "gateway-key")
		if w.Code != 503 || !strings.Contains(w.Body.String(), `"code":"RuntimeNotReady"`) {
			t.Fatalf("unknown tenant request: %d %s", w.Code, w.Body)
		}
	}
	if _, err := f.registry.Resolve(context.Background(), scope(missing)); !errors.Is(err, contract.ErrRuntimeNotReady) {
		t.Fatalf("request created tenant: %v", err)
	}
}
