package authkeys

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

// gatedDoer answers the key request only once it is released.
type gatedDoer struct {
	payload string
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedDoer(payload string) *gatedDoer {
	return &gatedDoer{payload: payload, entered: make(chan struct{}), release: make(chan struct{})}
}

func (d *gatedDoer) Do(r *http.Request) (*http.Response, error) {
	d.calls.Add(1)
	d.once.Do(func() { close(d.entered) })
	select {
	case <-d.release:
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(d.payload))}, nil
}

func flightClient(t *testing.T) (*Client, *gatedDoer) {
	t.Helper()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.FixedZone("MSK", 10800))
	key := make(ed25519.PublicKey, 32)
	key[0] = 7
	payload := `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"one","x":"` + base64.RawURLEncoding.EncodeToString(key) + `","alg":"EdDSA","use":"sig"}]}`
	doer := newGatedDoer(payload)
	client, err := NewClient(Options{URL: "http://auth/internal/auth/v1/jwks", ServiceKey: "key", Now: func() time.Time { return now }, HTTP: doer, AllowPlainHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return client, doer
}

// The caller that starts a refresh may give up; the refresh goes on for the
// others and its end is not read as a failure of Auth.
func TestCanceledInitiatorDoesNotPoisonRefresh(t *testing.T) {
	client, doer := flightClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	initiator := make(chan error, 1)
	go func() {
		_, err := client.Key(ctx, "one")
		initiator <- err
	}()
	<-doer.entered
	cancel()
	select {
	case err := <-initiator:
		if !errors.Is(err, contract.ErrAuthUnavailable) {
			t.Fatalf("canceled initiator = %v; want ErrAuthUnavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a canceled initiator kept waiting")
	}
	close(doer.release)
	got, err := client.Key(context.Background(), "one")
	if err != nil || got[0] != 7 {
		t.Fatalf("Key() after the initiator left = %v, %v; want the key", got, err)
	}
	if calls := doer.calls.Load(); calls != 1 {
		t.Fatalf("key requests = %d; want the one refresh to serve everybody", calls)
	}
}

func TestConcurrentKeyCallsOneRefresh(t *testing.T) {
	client, doer := flightClient(t)
	const callers = 12
	var started, finished sync.WaitGroup
	started.Add(callers)
	finished.Add(callers)
	failures := make(chan error, callers)
	for range callers {
		go func() {
			defer finished.Done()
			started.Done()
			if got, err := client.Key(context.Background(), "one"); err != nil || got[0] != 7 {
				failures <- err
			}
		}()
	}
	started.Wait()
	<-doer.entered
	time.Sleep(20 * time.Millisecond) // let the others reach the wait
	close(doer.release)
	finished.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("Key() = %v", err)
	}
	if calls := doer.calls.Load(); calls != 1 {
		t.Fatalf("key requests = %d for %d concurrent callers; want 1", calls, callers)
	}
}

// While a refresh is on the network, the cache lock is free: a second caller
// leaves at its own deadline instead of queueing behind the lock.
func TestRefreshDoesNotHoldCacheLock(t *testing.T) {
	client, doer := flightClient(t)
	first := make(chan error, 1)
	go func() {
		_, err := client.Key(context.Background(), "one")
		first <- err
	}()
	<-doer.entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Key(ctx, "one")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, contract.ErrAuthUnavailable) {
			t.Fatalf("Key() = %v; want ErrAuthUnavailable at the deadline", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a caller waited on the cache lock while the refresh was on the network")
	}
	close(doer.release)
	if err := <-first; err != nil {
		t.Fatalf("the refreshing caller = %v", err)
	}
}
