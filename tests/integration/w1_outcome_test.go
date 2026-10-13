package integration_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestW1BoundaryFailureOutcomesFollowOperation(t *testing.T) {
	operations := []struct {
		name, path, operation, permission, key, body, outcome string
	}{
		{"evaluation", "order-evaluations", "prepareOrder", "consumer.order.evaluate", "serving-key", `{"positions":[]}`, "unavailable"},
		{"mutation", "products", "registerProduct", "catalog.write", "gateway-key", `{"commandID":"c","productID":"P1","name":"milk","baseUnit":"g"}`, "not_committed"},
	}
	cases := []struct {
		name, code string
		status     int
	}{
		{"caller", "Unauthenticated", 401},
		{"missing authorization", "InvalidToken", 401},
		{"duplicate authorization", "InvalidToken", 401},
		{"signature", "InvalidToken", 401},
		{"permission", "Forbidden", 403},
		{"caller binding", "InvalidToken", 401},
		{"browser identity", "Forbidden", 403},
		{"missing epoch", "InvalidArgument", 400},
		{"invalid epoch", "InvalidArgument", 400},
		{"duplicate epoch", "InvalidArgument", 400},
		{"old epoch", "RuntimeReset", 409},
		{"discovery", "AuthUnavailable", 503},
		{"unregistered", "RuntimeNotReady", 503},
		{"lifecycle", "Forbidden", 403},
		{"query", "InvalidArgument", 400},
		{"body", "InvalidArgument", 400},
	}
	for _, operation := range operations {
		for _, tc := range cases {
			t.Run(operation.name+"/"+tc.name, func(t *testing.T) {
				f := setup(t)
				tenant := tenantA
				if tc.name == "unregistered" {
					tenant = "33333333-3333-4333-8333-333333333335"
				}
				c := claims(tenant, operation.operation, operation.permission)
				if operation.key == "serving-key" {
					c["aud"] = []string{"umir-serving", "umir-warehouse"}
				}
				switch tc.name {
				case "permission":
					c["permissions"] = []string{"menu.read"}
				case "caller binding":
					c["azp"] = "umir-auth"
				case "lifecycle":
					c["lifecycle_version"] = "2"
				}
				var header map[string]any
				if tc.name == "discovery" {
					header = map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": "unavailable"}
				}
				token := f.token(t, c, header)
				if tc.name == "signature" {
					token = token[:len(token)-8] + "AAAAAAAA"
				}
				body, path := operation.body, operation.path
				if tc.name == "query" {
					path += "?unexpected=true"
				}
				if tc.name == "body" {
					body = `{}`
				}
				r := httptest.NewRequest("POST", "/internal/warehouse/v2/r/"+tenant+"/"+path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Umir-Service-Key", operation.key)
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("X-Umir-Runtime-Epoch", epoch)
				switch tc.name {
				case "caller":
					r.Header.Set("X-Umir-Service-Key", "invalid-key")
				case "missing authorization":
					r.Header.Del("Authorization")
				case "duplicate authorization":
					r.Header.Add("Authorization", "Bearer "+token)
				case "browser identity":
					r.Header.Set("X-Umir-User-ID", "forged")
				case "missing epoch":
					r.Header.Del("X-Umir-Runtime-Epoch")
				case "invalid epoch":
					r.Header.Set("X-Umir-Runtime-Epoch", "invalid")
				case "duplicate epoch":
					r.Header.Add("X-Umir-Runtime-Epoch", epoch)
				case "old epoch":
					r.Header.Set("X-Umir-Runtime-Epoch", "44444444-4444-4444-8444-444444444445")
				}
				w := httptest.NewRecorder()
				f.handler.ServeHTTP(w, r)
				var response struct{ Code, Outcome string }
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				outcome := operation.outcome
				if tc.name == "old epoch" {
					outcome = "indeterminate"
				}
				if w.Code != tc.status || response.Code != tc.code || response.Outcome != outcome {
					t.Errorf("got %d %s; want %d %s/%s", w.Code, w.Body, tc.status, tc.code, outcome)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Error("boundary error is cacheable")
				}
				for _, tenant := range []string{tenantA, tenantB} {
					runtime, err := f.registry.Resolve(context.Background(), scope(tenant))
					if err != nil {
						t.Fatal(err)
					}
					history, err := runtime.GetHistory(context.Background())
					if err != nil || len(history.Events) != 0 {
						t.Fatalf("refused request wrote tenant %s history: %+v %v", tenant, history, err)
					}
				}
				if tc.name == "unregistered" {
					if _, err := f.registry.Resolve(context.Background(), scope(tenant)); err == nil {
						t.Fatal("refused request created runtime")
					}
				}
			})
		}
	}
}
