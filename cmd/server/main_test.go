package main

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
