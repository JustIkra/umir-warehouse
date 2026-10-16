package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/JustIkra/umir-warehouse/bootstrap"
)

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func healthcheck() error {
	client := &http.Client{Timeout: 2 * time.Second}
	return healthcheckWithClient(client, "http://127.0.0.1:"+env("PORT", "8080")+"/healthz")
}

func healthcheckWithClient(client *http.Client, address string) (err error) {
	response, err := client.Get(address)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, response.Body.Close())
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health status: %d", response.StatusCode)
	}
	return nil
}

func main() {
	if err := runMain(); err != nil {
		log.Printf("umir-warehouse: %v", err)
		os.Exit(1)
	}
}

func runMain() error {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		return healthcheck()
	}
	cfg, err := loadConfig(os.LookupEnv)
	if err != nil {
		return err
	}
	backend, err := buildBackend(cfg)
	if err != nil {
		return fmt.Errorf("create backend: %w", err)
	}
	publicServer := newHTTPServer(cfg.port, backend.public, cfg.requestTimeout)
	internalServer := newHTTPServer(cfg.internalPort, backend.internal, cfg.requestTimeout)
	servers := []*http.Server{publicServer, internalServer}
	shutdown := func(ctx context.Context) error {
		var shutdownErr error
		for _, server := range servers {
			shutdownErr = errors.Join(shutdownErr, server.Shutdown(ctx))
		}
		if shutdownErr != nil {
			for _, server := range servers {
				shutdownErr = errors.Join(shutdownErr, server.Close())
			}
		}
		return shutdownErr
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("umir-warehouse public listener on %s; internal listener on %s", publicServer.Addr, internalServer.Addr)
	return runService(signals, cfg, lifecycleDependencies{
		runSnapshotWorker: backend.snapshots,
		waitSnapshotRetry: waitForSnapshotRetry,
		serve:             func() error { return serveListeners(publicServer, internalServer) },
		shutdown:          shutdown,
	})
}

func newHTTPServer(port string, handler http.Handler, requestTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       requestTimeout,
		// A result recorded just before the request deadline still has time to be written.
		WriteTimeout: requestTimeout + 5*time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

func serveListeners(publicServer, internalServer *http.Server) error {
	results := make(chan error, 2)
	go func() { results <- publicServer.ListenAndServe() }()
	go func() { results <- internalServer.ListenAndServe() }()
	first := <-results
	if first != nil && !errors.Is(first, http.ErrServerClosed) {
		first = errors.Join(first, publicServer.Close(), internalServer.Close())
	}
	second := <-results
	return errors.Join(first, second)
}

type backendHandlers struct {
	public, internal http.Handler
	snapshots        func(context.Context) error
}

func buildBackend(cfg serverConfig) (backendHandlers, error) {
	options := bootstrap.Options{Now: time.Now, NewID: rand.Text, EventsPerSnapshot: cfg.eventsPerSnapshot}
	if cfg.apiMode == "legacy" {
		runtime, err := bootstrap.NewInMemory(options)
		if err != nil {
			return backendHandlers{}, err
		}
		if err := runtime.Recover(context.Background()); err != nil {
			return backendHandlers{}, err
		}
		public, internal, err := bootstrap.NewHTTPHandlers(runtime, bootstrap.HTTPOptions{RequestTimeout: cfg.requestTimeout, MaxRequestBytes: cfg.maxRequestBytes})
		return backendHandlers{public, internal, runtime.RunSnapshotWorker}, err
	}
	id, err := newEpoch()
	if err != nil {
		return backendHandlers{}, err
	}
	registry, err := bootstrap.NewRegistry(options, id)
	if err != nil {
		return backendHandlers{}, err
	}
	keys, err := bootstrap.NewAuthSigningKeys(bootstrap.AuthKeyOptions{URL: cfg.authJWKSURL, ServiceKey: cfg.authServiceKey, Now: time.Now, HTTP: &http.Client{Timeout: cfg.requestTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, AllowPlainHTTP: cfg.allowPlainHTTP})
	if err != nil {
		return backendHandlers{}, err
	}
	verifier, err := bootstrap.NewJWTVerifier(bootstrap.JWTOptions{Now: time.Now, Keys: keys})
	if err != nil {
		return backendHandlers{}, err
	}
	internal, err := bootstrap.NewScopedHTTPHandler(registry, bootstrap.ScopedHTTPOptions{RequestTimeout: cfg.requestTimeout, MaxRequestBytes: cfg.maxRequestBytes, Verifier: verifier, CallerKeys: cfg.callerKeys})
	if err != nil {
		return backendHandlers{}, err
	}
	return backendHandlers{bootstrap.NewHealthOnlyHandler(), internal, registry.RunSnapshotWorker}, nil
}
func newEpoch() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}
