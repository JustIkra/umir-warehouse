package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/tests/fixtures/protocol"
)

type serviceKeys struct{ Lifecycle, Gateway, Serving string }
type process struct {
	http.Handler
	registry  *bootstrap.Registry
	transport *http.Transport
	faults    *protocol.OwnerFaults
	signing   interface {
		Key(context.Context, string) (ed25519.PublicKey, error)
	}
}

func (p *process) CloseIdleConnections() { p.transport.CloseIdleConnections() }
func compose(config ownerConfig, keys serviceKeys, now func() time.Time, epoch string, client *http.Client) (*process, error) {
	registry, err := bootstrap.NewRegistry(bootstrap.Options{Now: now, NewID: rand.Text, EventsPerSnapshot: 100}, epoch)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{}
	if client == nil {
		client = &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	signing, err := bootstrap.NewAuthSigningKeys(bootstrap.AuthKeyOptions{URL: config.ServiceURLs.Auth + "/internal/auth/v1/jwks", ServiceKey: keys.Lifecycle, Now: now, RetryNow: time.Now, HTTP: client, AllowPlainHTTP: true})
	if err != nil {
		return nil, err
	}
	verifier, err := bootstrap.NewJWTVerifier(bootstrap.JWTOptions{Now: now, Keys: signing})
	if err != nil {
		return nil, err
	}
	faults := protocol.NewOwnerFaults(time.Now)
	directory := protocol.NewDirectory(registry, faults)
	handler, err := bootstrap.NewScopedHTTPHandler(directory, bootstrap.ScopedHTTPOptions{RequestTimeout: 5 * time.Second, MaxRequestBytes: 1 << 20, Verifier: verifier, CallerKeys: map[string]string{"umir-auth": keys.Lifecycle, "umir-gateway": keys.Gateway, "umir-serving": keys.Serving}})
	if err != nil {
		return nil, err
	}
	return &process{Handler: protocol.BusinessHandler(handler, faults), registry: registry, transport: transport, faults: faults, signing: signing}, nil
}
func ownerReady(ctx context.Context, app *process, key, epoch string) bool {
	if ctx.Err() != nil {
		return false
	}
	_, discoveryErr := app.signing.Key(ctx, "fixture-readiness-probe")
	if discoveryErr != nil && !errors.Is(discoveryErr, contract.ErrInvalidToken) {
		return false
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://warehouse/internal/warehouse/v2/runtime", nil)
	if err != nil {
		return false
	}
	r.Header.Set("X-Umir-Service-Key", key)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	var state struct {
		Service, Epoch, TZDataVersion string
		Ready                         bool
	}
	return w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &state) == nil && state.Service == "warehouse" && state.Epoch == epoch && state.Ready && state.TZDataVersion == bootstrap.TimeZoneDataVersion && ctx.Err() == nil
}
