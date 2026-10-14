package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProfileConfigStrictBinding(t *testing.T) {
	cfg := testConfig()
	raw := []byte(`{"amendment":"umir-owner-profiles/0.1.0-unpublished","project":"` + cfg.Project + `","profile":"warehouse-tzdata-missing","profileContractIdentity":"sha256:ee5ea052cf8239a40fdc3aba7c97c0a143260be284c390168a75c8afb7770460"}`)
	if _, _, err := parseProfileConfig(raw, cfg.Project, "warehouse-tzdata-missing"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{[]byte(strings.Replace(string(raw), `"amendment":`, `"extra":1,"amendment":`, 1)), []byte(string(raw) + `{}`), []byte(strings.Replace(string(raw), `warehouse-tzdata-missing`, `auth-rotation`, 1)), []byte(strings.Replace(string(raw), `"project":`, `"project":"wrong","project":`, 1))} {
		if _, _, err := parseProfileConfig(bad, cfg.Project, "warehouse-tzdata-missing"); err == nil {
			t.Fatal("invalid profile accepted")
		}
	}
}

func TestProfileCatalogFreshCASReplayAndGuards(t *testing.T) {
	f := testComposition(t)
	f.c.catalog = func(ctx context.Context) (catalogFact, error) {
		return observeCatalog(ctx, f.app, f.keys.Lifecycle, strings.Repeat("e", 64))
	}
	raw, err := json.Marshal(profileRequest{Amendment: profileAmendment, Project: f.c.project, RequestID: "99999999-9999-4999-8999-999999999999", ExpectedInstanceID: f.c.instance, ExpectedGeneration: "1", Action: "catalog-report"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(body []byte, key, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.c.ServeHTTP(w, r)
		return w
	}
	good := call(raw, f.c.credential, profilePath)
	if good.Code != 200 {
		t.Fatalf("catalog: %d %s", good.Code, good.Body)
	}
	if again := call(raw, f.c.credential, profilePath); again.Body.String() != good.Body.String() {
		t.Fatal("replay changed")
	}
	for _, bad := range [][]byte{[]byte(strings.Replace(string(raw), `catalog-report`, `report`, 1)), []byte(strings.Replace(string(raw), `"expectedGeneration":"1"`, `"expectedGeneration":"2"`, 1)), []byte(strings.Replace(string(raw), `"requestID":`, `"requestID":"bad","requestID":`, 1)), []byte(string(raw) + `{}`)} {
		if call(bad, f.c.credential, profilePath).Code == 200 {
			t.Fatal("bad request accepted")
		}
	}
	if call(raw, "wrong", profilePath).Code != 401 || call(raw, f.c.credential, profilePath+"?x=1").Code == 200 {
		t.Fatal("transport guard bypass")
	}
	if len(f.c.profileCache) != 1 {
		t.Fatal("invalid request consumed report capacity")
	}

}

func TestProfileCatalogRejectsProfileField(t *testing.T) {
	for _, selector := range []string{`""`, `null`, `"warehouse-tzdata-missing"`, `"auth-rotation"`} {
		t.Run(selector, func(t *testing.T) {
			f := testComposition(t)
			f.c.catalog = func(ctx context.Context) (catalogFact, error) {
				return observeCatalog(ctx, f.app, f.keys.Lifecycle, strings.Repeat("e", 64))
			}
			raw, err := json.Marshal(profileRequest{Amendment: profileAmendment, Project: f.c.project, RequestID: "99999999-9999-4999-8999-999999999999", ExpectedInstanceID: f.c.instance, ExpectedGeneration: "1", Action: "catalog-report"})
			if err != nil {
				t.Fatal(err)
			}
			call := func(body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, profilePath, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+f.c.credential)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				f.c.ServeHTTP(w, r)
				return w
			}
			invalid := strings.TrimSuffix(string(raw), "}") + `,"profile":` + selector + `}`
			for range 2 {
				w := call(invalid)
				var reply struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
					t.Fatal(err)
				}
				if w.Code != http.StatusBadRequest || reply.Error != "InvalidRequest" {
					t.Errorf("forbidden selector: status=%d error=%q", w.Code, reply.Error)
				}
			}
			if len(f.c.profileCache) != 0 {
				t.Error("invalid envelope consumed report capacity")
			}
			good := call(string(raw))
			if good.Code != http.StatusOK {
				t.Fatalf("valid envelope with same request ID: %d %s", good.Code, good.Body)
			}
			if replay := call(string(raw)); replay.Code != http.StatusOK || replay.Body.String() != good.Body.String() {
				t.Error("valid catalogue replay changed")
			}
		})
	}
}

func TestProfileFreshInstanceGenerationCapacityAndExpiry(t *testing.T) {
	f := testComposition(t)
	f.c.catalog = func(ctx context.Context) (catalogFact, error) {
		return observeCatalog(ctx, f.app, f.keys.Lifecycle, strings.Repeat("e", 64))
	}
	call := func(id, instance, generation string) *httptest.ResponseRecorder {
		raw, err := json.Marshal(profileRequest{Amendment: profileAmendment, Project: f.c.project, RequestID: id, ExpectedInstanceID: instance, ExpectedGeneration: generation, Action: "catalog-report"})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, profilePath, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+f.c.credential)
		w := httptest.NewRecorder()
		f.c.ServeHTTP(w, r)
		return w
	}
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	if call(id, "88888888-8888-4888-8888-888888888888", "1").Code != 409 || call(id, f.c.instance, "2").Code != 409 {
		t.Fatal("stale binding accepted")
	}
	for range 64 {
		id, err = newUUID()
		if err != nil {
			t.Fatal(err)
		}
		if call(id, f.c.instance, "1").Code != 200 {
			t.Fatal("capacity prematurely exhausted")
		}
	}
	id, err = newUUID()
	if err != nil {
		t.Fatal(err)
	}
	if call(id, f.c.instance, "1").Code != 413 {
		t.Fatal("profile capacity unbounded")
	}
	f.c.profileDeadline = time.Now().Add(-time.Second)
	if call(id, f.c.instance, "1").Code != 408 {
		t.Fatal("expired profile accepted")
	}
}

func TestRefusalControlsCannotMutateOwner(t *testing.T) {
	cfg := testConfig()
	c, err := newControl(cfg.Project, "11111111-1111-4111-8111-111111111111", cfg.FixtureID, cfg.InitialInstant, "1", strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64), func(context.Context) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	c.profileOnly = true
	before := c.readClock()
	for _, operation := range []string{"fixture.ready", "clock.set", "clock.checkpoint", "owner.fault.arm", "owner.fault.clear"} {
		_, code := c.execute(context.Background(), request{Operation: operation, Data: json.RawMessage(`{}`)})
		if code != "NotReady" {
			t.Fatalf("refusal accepted %s", operation)
		}
	}
	if c.readClock().Generation != before.Generation || c.readClock().Instant != before.Instant {
		t.Fatal("refusal clock mutated")
	}
	if _, code := c.execute(context.Background(), request{Operation: "clock.read", Data: json.RawMessage(`{}`)}); code != "" {
		t.Fatal("read-only clock unavailable")
	}
}
