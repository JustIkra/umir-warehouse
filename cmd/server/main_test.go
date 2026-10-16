package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
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

	runtime, err := bootstrap.NewInMemory(bootstrap.Options{Now: time.Now, NewID: func() string { return "id" }, EventsPerSnapshot: 1})
	if err != nil {
		t.Fatalf("NewInMemory() error = %v", err)
	}
	app, _, err := bootstrap.NewHTTPHandlers(runtime, bootstrap.HTTPOptions{RequestTimeout: time.Second, MaxRequestBytes: 1024})
	if err != nil {
		t.Fatalf("NewHTTPHandlers() error = %v", err)
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
	if cfg.port != "8080" || cfg.internalPort != "8082" || cfg.requestTimeout != 8*time.Second || cfg.maxRequestBytes != 1<<20 || cfg.eventsPerSnapshot != 100 || cfg.snapshotRetryInterval != time.Second {
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

func TestLoadConfigUsesSnapshotRetryIntervalAndIgnoresExpirationInterval(t *testing.T) {
	cfg, err := loadConfig(func(key string) (string, bool) {
		switch key {
		case "SNAPSHOT_RETRY_INTERVAL":
			return "250ms", true
		case "EXPIRATION_CHECK_INTERVAL":
			return "9s", true
		default:
			return "", false
		}
	})
	if err != nil || cfg.snapshotRetryInterval != 250*time.Millisecond {
		t.Fatalf("loadConfig() = %#v, %v; want snapshot retry 250ms", cfg, err)
	}
	cfg, err = loadConfig(func(key string) (string, bool) {
		if key == "EXPIRATION_CHECK_INTERVAL" {
			return "9s", true
		}
		return "", false
	})
	if err != nil || cfg.snapshotRetryInterval != time.Second {
		t.Fatalf("loadConfig(obsolete expiration setting) = %#v, %v; want 1s snapshot default", cfg, err)
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
		{name: "zero snapshot retry interval", key: "SNAPSHOT_RETRY_INTERVAL", value: "0s"},
		{name: "negative snapshot retry interval", key: "SNAPSHOT_RETRY_INTERVAL", value: "-1s"},
		{name: "invalid snapshot retry interval", key: "SNAPSHOT_RETRY_INTERVAL", value: "soon"},
		{name: "same listeners", key: "INTERNAL_PORT", value: "8080"},
		{name: "same listeners with leading zero", key: "INTERNAL_PORT", value: "08080"},
		{name: "invalid port", key: "PORT", value: "70000"},
		{name: "zero request timeout", key: "REQUEST_TIMEOUT", value: "0s"},
		{name: "invalid request timeout", key: "REQUEST_TIMEOUT", value: "soon"},
		{name: "zero body limit", key: "MAX_REQUEST_BYTES", value: "0"},
		{name: "too large body limit", key: "MAX_REQUEST_BYTES", value: "67108865"},
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

func TestServeListenersOwnsBothListenersUntilShutdown(t *testing.T) {
	publicPort := freePort(t)
	internalPort := freePort(t)
	app := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	publicServer := newHTTPServer(publicPort, app, time.Second)
	internalServer := newHTTPServer(internalPort, app, time.Second)
	served := make(chan error, 1)
	go func() { served <- serveListeners(publicServer, internalServer) }()
	client := &http.Client{Timeout: time.Second}
	for _, address := range []string{"http://127.0.0.1:" + publicPort, "http://127.0.0.1:" + internalPort} {
		waitForHTTP(t, client, address)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := errors.Join(publicServer.Shutdown(shutdownCtx), internalServer.Shutdown(shutdownCtx)); err != nil {
		t.Fatalf("Shutdown() = %v", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serveListeners() = %v, want ErrServerClosed", err)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address has type %T, want *net.TCPAddr", listener.Addr())
	}
	port := strconv.Itoa(address.Port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func waitForHTTP(t *testing.T, client *http.Client, address string) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		response, err := client.Get(address)
		if err == nil {
			if closeErr := response.Body.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("listener %s did not start: %v", address, err)
		case <-ticker.C:
		}
	}
}

func TestRunServiceCancellationJoinsSnapshotAndShutdown(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	snapshotStarted := make(chan struct{})
	retryWaitStarted := make(chan struct{})
	retryWaitFinished := make(chan struct{})
	snapshotDone := make(chan struct{})
	shutdownDone := make(chan struct{})
	serveErr := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- runService(parent, serverConfig{snapshotRetryInterval: 7 * time.Second}, lifecycleDependencies{
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
	<-snapshotStarted
	<-retryWaitStarted
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runService() error = %v", err)
	}
	<-snapshotDone
	<-retryWaitFinished
	<-shutdownDone
}

func TestRunServiceSnapshotInfrastructureFailureShutsDownAndReturnsErrors(t *testing.T) {
	snapshotErr := errors.New("snapshot worker failed unexpectedly")
	shutdownErr := errors.New("shutdown failed")
	shutdownCalled := make(chan struct{})
	err := runService(context.Background(), serverConfig{snapshotRetryInterval: time.Second}, lifecycleDependencies{
		runSnapshotWorker: func(context.Context) error { return snapshotErr },
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
}

func TestRunServiceRetriesSnapshotSaveWhileServing(t *testing.T) {
	storeErr := errors.New("temporary snapshot store failure")
	parent, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	retryWaiting := make(chan time.Duration, 1)
	allowRetry := make(chan struct{})
	retrySucceeded := make(chan struct{})
	serveStarted := make(chan struct{})
	serveExited := make(chan struct{})
	shutdownCalled := make(chan struct{})
	logs := make(chan string, 1)
	done := make(chan error, 1)
	var snapshotAttempts atomic.Int32
	go func() {
		done <- runService(parent, serverConfig{snapshotRetryInterval: 4 * time.Second}, lifecycleDependencies{
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
}

func TestRunServiceServeFailureCancelsWorkerAndShutsDown(t *testing.T) {
	serveErr := errors.New("listen failed")
	snapshotDone := make(chan struct{})
	shutdownCalled := make(chan struct{})
	err := runService(context.Background(), serverConfig{snapshotRetryInterval: time.Second}, lifecycleDependencies{
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
}

func TestRunServiceJoinsServeAndSnapshotFailures(t *testing.T) {
	serveErr := errors.New("listen failed")
	snapshotErr := errors.New("snapshot worker failed during shutdown")
	err := runService(context.Background(), serverConfig{snapshotRetryInterval: time.Second}, lifecycleDependencies{
		runSnapshotWorker: func(ctx context.Context) error {
			<-ctx.Done()
			return snapshotErr
		},
		waitSnapshotRetry: func(context.Context, time.Duration) error { return nil },
		serve:             func() error { return serveErr },
		shutdown:          func(context.Context) error { return nil },
		logf:              func(string, ...any) {},
	})
	if !errors.Is(err, serveErr) || !errors.Is(err, snapshotErr) {
		t.Fatalf("runService() error = %v; want serve and snapshot failures", err)
	}
}

func TestServerWriteTimeoutExceedsRequestTimeout(t *testing.T) {
	server := newHTTPServer("0", http.NotFoundHandler(), 8*time.Second)
	if server.WriteTimeout != 13*time.Second {
		t.Fatalf("WriteTimeout = %v; want the request timeout plus 5s so a recorded result can be written", server.WriteTimeout)
	}
	if server.ReadTimeout != 8*time.Second {
		t.Fatalf("ReadTimeout = %v; want the request timeout", server.ReadTimeout)
	}
}
