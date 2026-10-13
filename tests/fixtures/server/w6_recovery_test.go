package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestW6ReadinessRecoversWithoutClockAdvance(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	available := make(chan struct{})
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-available:
		default:
			w.WriteHeader(503)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": "real-shaped-unit-key", "x": base64.RawURLEncoding.EncodeToString(public), "alg": "EdDSA", "use": "sig"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer auth.Close()
	cfg := testConfig()
	cfg.ServiceURLs.Auth = auth.URL
	c, err := newControl(cfg.Project, "11111111-1111-4111-8111-111111111111", cfg.FixtureID, cfg.InitialInstant, cfg.ClockGeneration, strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64), func(context.Context) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	epoch := "44444444-4444-4444-8444-444444444444"
	app, err := compose(cfg, serviceKeys{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}, c.now, epoch, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.CloseIdleConnections()
	if ownerReady(context.Background(), app, strings.Repeat("a", 64), epoch) {
		t.Fatal("unavailable Auth was reported ready")
	}
	close(available)
	initial := c.now()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	recovered := false
	for !recovered {
		select {
		case <-deadline.C:
			t.Fatal("discovery did not recover within bounded host retry budget")
		case <-ticker.C:
			recovered = ownerReady(context.Background(), app, strings.Repeat("a", 64), epoch)
		}
	}
	if !c.now().Equal(initial) {
		t.Fatal("recovery advanced authoritative time")
	}
	if !recovered {
		t.Fatal("transient Auth failure permanently blocks readiness while the fixture clock stays fixed")
	}
}
