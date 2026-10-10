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

	"github.com/JustIkra/umir-warehouse/internal/application"
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
	retryWaitStarted := make(chan struct{})
	retryWaitFinished := make(chan struct{})
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
				return &application.SnapshotSaveError{Err: errors.New("temporary snapshot save failure")}
			},
			waitSnapshotRetry: func(ctx context.Context, delay time.Duration) error {
				if delay != 7*time.Second {
					t.Errorf("snapshot retry delay = %s; want 7s", delay)
				}
				close(retryWaitStarted)
				<-ctx.Done()
				close(retryWaitFinished)
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
	<-retryWaitStarted
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runService() error = %v", err)
	}
	<-expirationDone
	<-snapshotDone
	<-retryWaitFinished
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
			waitSnapshotRetry: func(context.Context, time.Duration) error { return nil },
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

func TestRunServiceSnapshotInfrastructureFailureShutsDownAndReturnsErrors(t *testing.T) {
	snapshotErr := errors.New("snapshot worker failed unexpectedly")
	shutdownErr := errors.New("shutdown failed")
	shutdownCalled := make(chan struct{})
	tickerFake := &controlledTicker{ticks: make(chan time.Time)}
	err := runService(context.Background(), serverConfig{expirationCheckInterval: time.Second}, lifecycleDependencies{
		newTicker:             func(time.Duration) ticker { return tickerFake },
		processDueExpirations: func(context.Context) error { return nil },
		runSnapshotWorker:     func(context.Context) error { return snapshotErr },
		waitSnapshotRetry: func(context.Context, time.Duration) error {
			t.Fatal("unexpected retry for infrastructure failure")
			return nil
		},
		serve:    func() error { <-shutdownCalled; return http.ErrServerClosed },
		shutdown: func(context.Context) error { close(shutdownCalled); return shutdownErr },
		logf:     func(string, ...any) {},
	})
	if !errors.Is(err, snapshotErr) || !errors.Is(err, shutdownErr) {
		t.Fatalf("runService() error = %v; want snapshot and shutdown errors", err)
	}
	if !tickerFake.stopped.Load() {
		t.Fatal("expiration ticker was not stopped after snapshot failure")
	}
}

func TestRunServiceRetriesSnapshotSaveWhileServingAndExpiryContinue(t *testing.T) {
	storeErr := errors.New("temporary snapshot store failure")
	parent, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tickerFake := &controlledTicker{ticks: make(chan time.Time, 1)}
	retryWaiting := make(chan time.Duration, 1)
	allowRetry := make(chan struct{})
	expirationCalls := make(chan struct{}, 1)
	retrySucceeded := make(chan struct{})
	serveStarted := make(chan struct{})
	serveExited := make(chan struct{})
	shutdownCalled := make(chan struct{})
	logs := make(chan string, 1)
	done := make(chan error, 1)
	var snapshotAttempts atomic.Int32
	go func() {
		done <- runService(parent, serverConfig{expirationCheckInterval: 4 * time.Second}, lifecycleDependencies{
			newTicker: func(time.Duration) ticker { return tickerFake },
			processDueExpirations: func(context.Context) error {
				expirationCalls <- struct{}{}
				return nil
			},
			runSnapshotWorker: func(ctx context.Context) error {
				switch snapshotAttempts.Add(1) {
				case 1:
					return &application.SnapshotSaveError{Err: storeErr}
				case 2:
					close(retrySucceeded)
					<-ctx.Done()
					return ctx.Err()
				default:
					return errors.New("snapshot worker restarted more than once")
				}
			},
			waitSnapshotRetry: func(waitCtx context.Context, delay time.Duration) error {
				retryWaiting <- delay
				select {
				case <-allowRetry:
					return nil
				case <-waitCtx.Done():
					return waitCtx.Err()
				}
			},
			serve: func() error {
				close(serveStarted)
				<-shutdownCalled
				close(serveExited)
				return http.ErrServerClosed
			},
			shutdown: func(context.Context) error { close(shutdownCalled); return nil },
			logf:     func(format string, args ...any) { logs <- fmt.Sprintf(format, args...) },
		})
	}()
	<-serveStarted
	if delay := <-retryWaiting; delay != 4*time.Second {
		t.Fatalf("snapshot retry delay = %s, want configured interval 4s", delay)
	}
	if message := <-logs; !strings.Contains(message, storeErr.Error()) {
		t.Fatalf("snapshot retry log = %q, want store failure", message)
	}
	select {
	case <-serveExited:
		t.Fatal("HTTP service stopped during snapshot retry wait")
	default:
	}

	tickerFake.ticks <- time.Time{}
	<-expirationCalls

	close(allowRetry)
	select {
	case <-retrySucceeded:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot worker retry did not start")
	}
	if snapshotAttempts.Load() != 2 {
		t.Fatalf("snapshot worker attempts = %d, want one failed attempt and one retry", snapshotAttempts.Load())
	}
	select {
	case <-serveExited:
		t.Fatal("HTTP service stopped after snapshot retry succeeded")
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runService() error after cancellation = %v", err)
	}
	<-serveExited
	if !tickerFake.stopped.Load() {
		t.Fatal("expiration ticker was not stopped after cancellation")
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
		waitSnapshotRetry: func(context.Context, time.Duration) error { return nil },
		serve:             func() error { return serveErr },
		shutdown:          func(context.Context) error { close(shutdownCalled); return nil },
		logf:              func(string, ...any) {},
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
