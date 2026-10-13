package application

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/httpapi"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestW3ReviewScopedIntakeUsesFrozenV2Schema(t *testing.T) {
	expiry := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, expiry.Add(time.Hour), 10000, 0)
	owner := w3Owner(t, store, &testClock{now: expiry})
	_, err := owner.ReceiveDelivery(context.Background(), contract.ReceiveDeliveryRequest{CommandID: "review-expired-intake", DeliveryID: "review-expired-intake", Number: "2", CreatedAt: expiry, Lines: []contract.DeliveryLine{{ProductID: "p", Quantity: 100, ManufacturedAt: expiry.Add(-24 * time.Hour), ReceivedAt: expiry, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}})
	if err != nil {
		t.Fatal(err)
	}
	batches, err := store.ReadBatches(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range batches {
		if b.CommandID != "review-expired-intake" {
			continue
		}
		if len(b.Events) != 2 || b.Events[1].Type != domain.EventBatchExpiryPending {
			t.Fatalf("not a real atomic expired intake: %+v", b)
		}
		t.Logf("real intake schemas=%d/%d captured settings=%s/%s", b.Events[0].SchemaVersion, b.Events[1].SchemaVersion, b.Events[0].Payload.Batches[0].AppliedTimeZone, b.Events[0].Payload.Batches[0].AppliedSettingsVersion)
		if b.Events[0].SchemaVersion != 2 {
			t.Errorf("frozen C2 DeliveryReceivedV2Event requires schemaVersion=2; got %d", b.Events[0].SchemaVersion)
		}
		recovered := w3Owner(t, store, &testClock{now: expiry})
		state, err := recovered.Prepare(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		batch := state.State.Batches[b.Events[0].Payload.Batches[0].BatchID]
		if !batch.PendingWriteoff || batch.RemainingQuantity != 100 || batch.AppliedTimeZone != "Europe/Moscow" || batch.AppliedSettingsVersion != "1" || batch.TZDataVersion == "" {
			t.Fatalf("v2 replay lost captured physical stock: %+v", batch)
		}
		return
	}
	t.Fatal("intake missing")
}

type reviewRejectStore struct {
	EventStore
	armed        bool
	pendingCalls int
	rejectAt     int
}

func (s *reviewRejectStore) AppendBatch(ctx context.Context, b domain.CommandBatch) (domain.AppendResult, error) {
	if s.armed && b.CommandType == "PrepareExpiry" {
		s.pendingCalls++
		rejectAt := s.rejectAt
		if rejectAt == 0 {
			rejectAt = 2
		}
		if s.pendingCalls == rejectAt {
			return domain.AppendResult{}, errors.New("synthetic pending append refusal")
		}
	}
	return s.EventStore.AppendBatch(ctx, b)
}

type reviewRuntime struct {
	httpapi.Runtime
	owner *Owner
}

func (r reviewRuntime) RunExpirations(ctx context.Context) (contract.ExpirationRun, error) {
	return r.owner.RunExpirations(ctx)
}
func (r reviewRuntime) ConfirmWriteoff(ctx context.Context, v contract.ConfirmWriteoffRequest) (contract.WriteoffResult, error) {
	return r.owner.ConfirmWriteoff(ctx, v)
}
func (r reviewRuntime) ResolveWriteoff(ctx context.Context, a, b string) (contract.WriteoffObservation, error) {
	return r.owner.ResolveWriteoff(ctx, a, b)
}
func (r reviewRuntime) PrepareStock(ctx context.Context) (contract.StockPreparation, error) {
	return r.owner.PrepareStock(ctx)
}
func (r reviewRuntime) CalculateStock(ctx context.Context, v contract.StockPreparation) (contract.InventoryView, error) {
	return CalculateStock(ctx, v)
}

type reviewDirectory struct{ r reviewRuntime }

func (d reviewDirectory) Epoch() string { return w3Scope().RuntimeEpoch }
func (d reviewDirectory) RuntimeFor(context.Context, contract.Scope) (httpapi.Runtime, error) {
	return d.r, nil
}

type reviewVerifier struct{ operation string }

func (v reviewVerifier) Validate(context.Context, string, string, string, string, string) (contract.Identity, error) {
	if v.operation == "expiry.due" {
		return contract.Identity{Scope: w3Scope(), Subject: "service:gateway", Kind: "service", Operation: v.operation, Permissions: []string{"expiry.due"}}, nil
	}
	return contract.Identity{Scope: w3Scope(), Subject: w3Actor, Kind: "user", Operation: v.operation, Permissions: []string{"writeoff.confirm"}}, nil
}
func reviewCall(t *testing.T, owner *Owner, op, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	h, err := httpapi.NewScopedHandler(reviewDirectory{reviewRuntime{owner: owner}}, httpapi.ScopedOptions{RequestTimeout: time.Second, MaxRequestBytes: 1024, CallerKeys: map[string]string{"umir-gateway": "synthetic-gateway"}, Verifier: reviewVerifier{op}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/warehouse/v2/r/"+w3Scope().RestaurantID+"/"+path, strings.NewReader(body))
	req.Header.Set("X-Umir-Service-Key", "synthetic-gateway")
	req.Header.Set("X-Umir-Runtime-Epoch", w3Scope().RuntimeEpoch)
	req.Header.Set("Authorization", "Bearer synthetic-local-verifier-input")
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	return out
}
func TestW3ReviewPartialDueRunRetainsCommittedInvalidation(t *testing.T) {
	expiry := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, expiry, 10000, 0)
	store := &reviewRejectStore{EventStore: base}
	clock := &testClock{now: expiry.Add(-time.Hour)}
	owner := w3Owner(t, store, clock)
	_, err := owner.ReceiveDelivery(context.Background(), contract.ReceiveDeliveryRequest{CommandID: "second-intake", DeliveryID: "second-intake", Number: "2", CreatedAt: clock.Now(), Lines: []contract.DeliveryLine{{ProductID: "p", Quantity: 100, ManufacturedAt: expiry.Add(-24 * time.Hour), ReceivedAt: clock.Now(), ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := base.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(expiry)
	store.armed = true
	out := reviewCall(t, owner, "expiry.due", "expiry-runs", `{"runID":"77777777-7777-4777-8777-777777777777"}`)
	after, err := base.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Code != 503 || after != before+1 || store.pendingCalls != 2 {
		t.Fatalf("counterexample setup status=%d versions=%d/%d pending attempts=%d", out.Code, before, after, store.pendingCalls)
	}
	t.Logf("real partial commit versions=%d/%d status=%d invalidation=%q committed-version=%q", before, after, out.Code, out.Header().Get("X-Umir-Invalidation-Required"), out.Header().Get("X-Umir-Committed-Version"))
	if out.Header().Get("X-Umir-Invalidation-Required") != "true" || out.Header().Get("X-Umir-Committed-Version") != strconv.FormatInt(after, 10) || out.Header().Get("X-Umir-Runtime-Epoch") != w3Scope().RuntimeEpoch {
		t.Error("committed pending transition hidden by later due-run failure")
	}
}
func TestW3ReviewRefusedConfirmationRetainsPreparationInvalidation(t *testing.T) {
	expiry := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, expiry, 10000, 0)
	owner := w3Owner(t, base, &testClock{now: expiry})
	before, err := base.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := reviewCall(t, owner, "confirmWriteoff", "writeoffs", `{"commandID":"missing-confirmation","batchID":"missing-batch"}`)
	after, err := base.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Code != 404 || after != before+1 {
		t.Fatalf("counterexample setup status=%d versions=%d/%d", out.Code, before, after)
	}
	t.Logf("real pending preparation versions=%d/%d refusal=%d invalidation=%q", before, after, out.Code, out.Header().Get("X-Umir-Invalidation-Required"))
	if out.Header().Get("X-Umir-Invalidation-Required") != "true" || out.Header().Get("X-Umir-Committed-Version") != strconv.FormatInt(after, 10) || out.Header().Get("X-Umir-Runtime-Epoch") != w3Scope().RuntimeEpoch {
		t.Error("confirmation refusal hides unrelated real pending preparation")
	}
	if !strings.Contains(out.Body.String(), `"outcome":"not_committed"`) {
		t.Fatal("preparation changed refused confirmation outcome")
	}
}

func TestW3RepairFailureBeforePreparationHasNoInvalidation(t *testing.T) {
	for _, op := range []string{"expiry.due", "confirmWriteoff"} {
		t.Run(op, func(t *testing.T) {
			expiry := moscowTime(2026, time.April, 2, 10)
			base := seedStore(t, expiry, 10000, 0)
			store := &reviewRejectStore{EventStore: base, rejectAt: 1}
			owner := w3Owner(t, store, &testClock{now: expiry})
			before, err := base.Version(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			store.armed = true
			route, body := "expiry-runs", `{"runID":"77777777-7777-4777-8777-777777777777"}`
			if op == "confirmWriteoff" {
				route, body = "writeoffs", `{"commandID":"refused-before-prepare","batchID":"missing-batch"}`
			}
			out := reviewCall(t, owner, op, route, body)
			after, err := base.Version(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if out.Code != 503 || after != before || out.Header().Get("X-Umir-Invalidation-Required") != "" || out.Header().Get("X-Umir-Committed-Version") != "" {
				t.Fatalf("uncommitted preparation advertised invalidation: status=%d versions=%d/%d headers=%v", out.Code, before, after, out.Header())
			}
		})
	}
}

func TestW3RepairConfirmationPreparationFailureRetainsVersion(t *testing.T) {
	expiry := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, expiry, 10000, 0)
	store := &reviewRejectStore{EventStore: base}
	clock := &testClock{now: expiry.Add(-time.Hour)}
	owner := w3Owner(t, store, clock)
	_, err := owner.ReceiveDelivery(context.Background(), contract.ReceiveDeliveryRequest{CommandID: "repair-second-intake", DeliveryID: "repair-second-intake", Number: "2", CreatedAt: clock.Now(), Lines: []contract.DeliveryLine{{ProductID: "p", Quantity: 100, ManufacturedAt: expiry.Add(-24 * time.Hour), ReceivedAt: clock.Now(), ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := base.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(expiry)
	store.armed = true
	out := reviewCall(t, owner, "confirmWriteoff", "writeoffs", `{"commandID":"repair-partial-confirmation","batchID":"missing-batch"}`)
	after, err := base.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Code != 503 || after != before+1 || out.Header().Get("X-Umir-Invalidation-Required") != "true" || out.Header().Get("X-Umir-Committed-Version") != strconv.FormatInt(after, 10) {
		t.Fatalf("partial confirmation preparation lost invalidation: status=%d versions=%d/%d headers=%v", out.Code, before, after, out.Header())
	}
}

func TestW3RepairConfirmationWithoutPreparationKeepsRefusalUncommitted(t *testing.T) {
	expiry := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, expiry, 10000, 0)
	owner := w3Owner(t, base, &testClock{now: expiry.Add(-time.Hour)})
	before, err := base.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := reviewCall(t, owner, "confirmWriteoff", "writeoffs", `{"commandID":"repair-available-confirmation","batchID":"batch"}`)
	after, err := base.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Code != 409 || before != after || out.Header().Get("X-Umir-Invalidation-Required") != "" || !strings.Contains(out.Body.String(), `"outcome":"not_committed"`) {
		t.Fatalf("available refusal changed stock or outcome: status=%d versions=%d/%d", out.Code, before, after)
	}
}

func TestW3RepairFreshConfirmationRetryRetainsOriginalResult(t *testing.T) {
	expiry := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, expiry, 10000, 0)
	owner := w3Owner(t, base, &testClock{now: expiry})
	request := contract.ConfirmWriteoffRequest{CommandID: "repair-fresh-confirmation", BatchID: "batch", ActorUserID: w3Actor}
	original, err := owner.ConfirmWriteoff(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := owner.ConfirmWriteoff(context.Background(), request)
	if err != nil || original != replay {
		t.Fatalf("per-call preparation changed retained confirmation: %+v/%+v %v", original, replay, err)
	}
}
