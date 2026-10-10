package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
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

func handler() (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "umir-warehouse"}); err != nil {
			log.Printf("health response: %v", err)
		}
	})
	return mux, nil
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
	app, err := handler()
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
	server := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           app,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("umir-warehouse listening on %s", server.Addr)
	return runService(signals, cfg, lifecycleDependencies{
		newTicker: func(interval time.Duration) ticker { return realTicker{ticker: time.NewTicker(interval)} },
		processDueExpirations: func(ctx context.Context) error {
			_, err := runtime.ProcessDueExpirations(ctx)
			return err
		},
		runSnapshotWorker: runtime.RunSnapshotWorker,
		serve:             server.ListenAndServe,
		shutdown:          server.Shutdown,
	})
}
