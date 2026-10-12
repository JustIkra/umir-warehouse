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
	runtime, err := bootstrap.NewInMemory(bootstrap.Options{
		Now:               time.Now,
		NewID:             rand.Text,
		EventsPerSnapshot: cfg.eventsPerSnapshot,
	})
	if err != nil {
		return fmt.Errorf("create in-memory runtime: %w", err)
	}
	if err := runtime.Recover(context.Background()); err != nil {
		return fmt.Errorf("recover in-memory runtime: %w", err)
	}
	publicHandler, internalHandler, err := bootstrap.NewHTTPHandlers(runtime, bootstrap.HTTPOptions{RequestTimeout: cfg.requestTimeout, MaxRequestBytes: cfg.maxRequestBytes})
	if err != nil {
		return fmt.Errorf("create HTTP handlers: %w", err)
	}
	publicServer := newHTTPServer(cfg.port, publicHandler, cfg.requestTimeout)
	internalServer := newHTTPServer(cfg.internalPort, internalHandler, cfg.requestTimeout)
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
		runSnapshotWorker: runtime.RunSnapshotWorker,
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
		WriteTimeout:      requestTimeout,
		IdleTimeout:       60 * time.Second,
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
