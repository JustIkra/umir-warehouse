package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type failingResponseWriter struct {
	header http.Header
	err    error
}

func (w *failingResponseWriter) Header() http.Header {
	return w.header
}

func (*failingResponseWriter) WriteHeader(int) {}

func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type failingCloseBody struct {
	err error
}

func (*failingCloseBody) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (b *failingCloseBody) Close() error {
	return b.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestHealthHandlerLogsResponseWriteFailure(t *testing.T) {
	writeErr := errors.New("response write failed")
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousWriter) })

	app, err := handler()
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	app.ServeHTTP(&failingResponseWriter{header: make(http.Header), err: writeErr}, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if !strings.Contains(logs.String(), writeErr.Error()) {
		t.Fatalf("logs = %q; want response write error", logs.String())
	}
}

func TestHealthcheckReturnsBodyCloseFailure(t *testing.T) {
	closeErr := errors.New("response body close failed")
	client := healthcheckClient(http.StatusOK, closeErr)

	err := healthcheckWithClient(client, "http://warehouse.test/healthz")
	if !errors.Is(err, closeErr) {
		t.Fatalf("healthcheckWithClient() error = %v; want body close error", err)
	}
}

func TestHealthcheckJoinsStatusAndCloseFailures(t *testing.T) {
	closeErr := errors.New("response body close failed")
	client := healthcheckClient(http.StatusServiceUnavailable, closeErr)

	err := healthcheckWithClient(client, "http://warehouse.test/healthz")
	if !errors.Is(err, closeErr) {
		t.Fatalf("healthcheckWithClient() error = %v; want body close error", err)
	}
	if !strings.Contains(err.Error(), "health status: 503") {
		t.Fatalf("healthcheckWithClient() error = %v; want health status error", err)
	}
}

func healthcheckClient(statusCode int, closeErr error) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: statusCode, Body: &failingCloseBody{err: closeErr}}, nil
	})}
}

func TestLoadConfigDefaultsAndPort(t *testing.T) {
	cfg, err := loadConfig(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.port != "8080" || cfg.eventsPerSnapshot != 100 || cfg.expirationCheckInterval != time.Second {
		t.Fatalf("loadConfig() = %#v; want defaults", cfg)
	}

	cfg, err = loadConfig(func(key string) (string, bool) {
		if key == "PORT" {
			return "18080", true
		}
		return "", false
	})
	if err != nil || cfg.port != "18080" {
		t.Fatalf("loadConfig(PORT) = %#v, %v; want port 18080", cfg, err)
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "zero snapshot threshold", key: "EVENTS_PER_SNAPSHOT", value: "0"},
		{name: "negative snapshot threshold", key: "EVENTS_PER_SNAPSHOT", value: "-1"},
		{name: "non-number snapshot threshold", key: "EVENTS_PER_SNAPSHOT", value: "many"},
		{name: "overflow snapshot threshold", key: "EVENTS_PER_SNAPSHOT", value: "9223372036854775808"},
		{name: "zero interval", key: "EXPIRATION_CHECK_INTERVAL", value: "0s"},
		{name: "negative interval", key: "EXPIRATION_CHECK_INTERVAL", value: "-1s"},
		{name: "invalid interval", key: "EXPIRATION_CHECK_INTERVAL", value: "soon"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfig(func(key string) (string, bool) {
				if key == tt.key {
					return tt.value, true
				}
				return "", false
			})
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("loadConfig() error = %v; want error naming %s", err, tt.key)
			}
		})
	}
}

type controlledTicker struct {
	ticks   chan time.Time
	stopped atomic.Bool
}

func (t *controlledTicker) C() <-chan time.Time { return t.ticks }
func (t *controlledTicker) Stop()               { t.stopped.Store(true) }

func TestRunServiceCancellationJoinsWorkersAndShutdown(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tickerFake := &controlledTicker{ticks: make(chan time.Time, 1)}
	tickerFake.ticks <- time.Now()
	expirationStarted := make(chan struct{})
	snapshotStarted := make(chan struct{})
	expirationDone := make(chan struct{})
	snapshotDone := make(chan struct{})
	shutdownDone := make(chan struct{})
	serveErr := make(chan error, 1)

	done := make(chan error, 1)
	go func() {
		done <- runService(parent, serverConfig{expirationCheckInterval: 7 * time.Second}, lifecycleDependencies{
			newTicker: func(interval time.Duration) ticker {
				if interval != 7*time.Second {
					t.Errorf("ticker interval = %s; want 7s", interval)
				}
				return tickerFake
			},
			processDueExpirations: func(ctx context.Context) error {
				close(expirationStarted)
				<-ctx.Done()
				close(expirationDone)
				return ctx.Err()
			},
			runSnapshotWorker: func(ctx context.Context) error {
				close(snapshotStarted)
				<-ctx.Done()
				close(snapshotDone)
				return ctx.Err()
			},
			serve: func() error { return <-serveErr },
			shutdown: func(context.Context) error {
				close(shutdownDone)
				serveErr <- http.ErrServerClosed
				return nil
			},
			logf: func(string, ...any) {},
		})
	}()
	<-expirationStarted
	<-snapshotStarted
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runService() error = %v", err)
	}
	<-expirationDone
	<-snapshotDone
	<-shutdownDone
	if !tickerFake.stopped.Load() {
		t.Fatal("ticker was not stopped")
	}
}

func TestRunServiceRetriesExpirationAfterError(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tickerFake := &controlledTicker{ticks: make(chan time.Time, 2)}
	called := make(chan int, 2)
	var calls atomic.Int32
	logs := make(chan string, 1)
	serveErr := make(chan error, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- runService(parent, serverConfig{expirationCheckInterval: time.Second}, lifecycleDependencies{
			newTicker: func(time.Duration) ticker { return tickerFake },
			processDueExpirations: func(context.Context) error {
				count := int(calls.Add(1))
				called <- count
				if count == 1 {
					return errors.New("temporary expiration failure")
				}
				return nil
			},
			runSnapshotWorker: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
			serve:             func() error { return <-serveErr },
			shutdown:          func(context.Context) error { serveErr <- http.ErrServerClosed; return nil },
			logf:              func(format string, args ...any) { logs <- fmt.Sprintf(format, args...) },
		})
	}()
	tickerFake.ticks <- time.Now()
	if got := <-called; got != 1 {
		t.Fatalf("first expiration call = %d; want 1", got)
	}
	if got := <-logs; !strings.Contains(got, "temporary expiration failure") {
		t.Fatalf("expiration log = %q; want callback error", got)
	}
	tickerFake.ticks <- time.Now()
	if got := <-called; got != 2 {
		t.Fatalf("second expiration call = %d; want retry", got)
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatalf("runService() error = %v", err)
	}
}

func TestRunServiceSnapshotFailureShutsDownAndReturnsErrors(t *testing.T) {
	snapshotErr := errors.New("snapshot worker failed")
	shutdownErr := errors.New("shutdown failed")
	shutdownCalled := make(chan struct{})
	tickerFake := &controlledTicker{ticks: make(chan time.Time)}
	err := runService(context.Background(), serverConfig{expirationCheckInterval: time.Second}, lifecycleDependencies{
		newTicker:             func(time.Duration) ticker { return tickerFake },
		processDueExpirations: func(context.Context) error { return nil },
		runSnapshotWorker:     func(context.Context) error { return snapshotErr },
		serve:                 func() error { <-shutdownCalled; return http.ErrServerClosed },
		shutdown:              func(context.Context) error { close(shutdownCalled); return shutdownErr },
		logf:                  func(string, ...any) {},
	})
	if !errors.Is(err, snapshotErr) || !errors.Is(err, shutdownErr) {
		t.Fatalf("runService() error = %v; want snapshot and shutdown errors", err)
	}
	if !tickerFake.stopped.Load() {
		t.Fatal("expiration ticker was not stopped after snapshot failure")
	}
}

func TestRunServiceServeFailureCancelsWorkersAndShutsDown(t *testing.T) {
	serveErr := errors.New("listen failed")
	snapshotDone := make(chan struct{})
	shutdownCalled := make(chan struct{})
	tickerFake := &controlledTicker{ticks: make(chan time.Time)}
	err := runService(context.Background(), serverConfig{expirationCheckInterval: time.Second}, lifecycleDependencies{
		newTicker:             func(time.Duration) ticker { return tickerFake },
		processDueExpirations: func(context.Context) error { return nil },
		runSnapshotWorker: func(ctx context.Context) error {
			<-ctx.Done()
			close(snapshotDone)
			return ctx.Err()
		},
		serve:    func() error { return serveErr },
		shutdown: func(context.Context) error { close(shutdownCalled); return nil },
		logf:     func(string, ...any) {},
	})
	if !errors.Is(err, serveErr) {
		t.Fatalf("runService() error = %v; want serve error", err)
	}
	<-snapshotDone
	<-shutdownCalled
	if !tickerFake.stopped.Load() {
		t.Fatal("expiration ticker was not stopped after serve failure")
	}
}
