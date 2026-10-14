package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestW1V2ConfigFailsClosed(t *testing.T) {
	for _, values := range []map[string]string{
		{"WAREHOUSE_API_MODE": "other"},
		{"WAREHOUSE_API_MODE": "v2"},
		{"WAREHOUSE_API_MODE": "v2", "AUTH_JWKS_URL": "http://auth/internal/auth/v1/jwks", "AUTH_SERVICE_KEY": "own", "GATEWAY_SERVICE_KEY": "gateway", "SERVING_SERVICE_KEY": "serving", "AUTH_RUNTIME_SERVICE_KEY": "runtime"},
	} {
		if _, err := loadConfig(func(key string) (string, bool) { v, ok := values[key]; return v, ok }); err == nil {
			t.Fatalf("unsafe config accepted: mode=%s", values["WAREHOUSE_API_MODE"])
		}
	}
	values := map[string]string{"WAREHOUSE_API_MODE": "v2", "AUTH_JWKS_URL": "http://auth/internal/auth/v1/jwks", "AUTH_SERVICE_KEY": "own", "GATEWAY_SERVICE_KEY": "gateway", "SERVING_SERVICE_KEY": "serving", "AUTH_RUNTIME_SERVICE_KEY": "runtime", "AUTH_ALLOW_PLAIN_HTTP": "true"}
	if _, err := loadConfig(func(key string) (string, bool) { v, ok := values[key]; return v, ok }); err != nil {
		t.Fatal(err)
	}
}

func TestW1V2ListenersKeepBusinessPrivate(t *testing.T) {
	values := map[string]string{"WAREHOUSE_API_MODE": "v2", "AUTH_JWKS_URL": "http://auth/internal/auth/v1/jwks", "AUTH_SERVICE_KEY": "own", "GATEWAY_SERVICE_KEY": "gateway", "SERVING_SERVICE_KEY": "serving", "AUTH_RUNTIME_SERVICE_KEY": "runtime", "AUTH_ALLOW_PLAIN_HTTP": "true"}
	cfg, err := loadConfig(func(key string) (string, bool) { v, ok := values[key]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	backend, err := buildBackend(cfg)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/internal/warehouse/v2/r/33333333-3333-4333-8333-333333333333/products", nil)
	for _, tc := range []struct {
		handler http.Handler
		want    int
	}{{backend.public, 400}, {backend.internal, 401}} {
		w := httptest.NewRecorder()
		tc.handler.ServeHTTP(w, request)
		if w.Code != tc.want {
			t.Fatalf("listener %d want %d", w.Code, tc.want)
		}
	}
}
