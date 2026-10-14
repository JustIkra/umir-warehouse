package integration_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
)

func newW2Registry(t *testing.T, runtimeEpoch string, clock func() time.Time) *bootstrap.Registry {
	t.Helper()
	r, err := bootstrap.NewRegistry(bootstrap.Options{Now: clock, NewID: rand.Text, EventsPerSnapshot: 1}, runtimeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestW2RestartEmptyStateAndCoverage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r, err := f.registry.Resolve(ctx, scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "same", ProductID: "P1", Name: "rice", BaseUnit: "g"}); err != nil {
		t.Fatal(err)
	}
	oldCoverage, err := f.registry.Coverage(ctx, scope(tenantA))
	if err != nil || !oldCoverage.Equal(now) {
		t.Fatalf("coverage %v %v", oldCoverage, err)
	}
	nextEpoch := "44444444-4444-4444-8444-444444444445"
	nextStart := now.Add(24 * time.Hour)
	restarted := newW2Registry(t, nextEpoch, func() time.Time { return nextStart })
	if _, err := restarted.Resolve(ctx, scope(tenantA)); !errors.Is(err, contract.ErrRuntimeReset) {
		t.Fatalf("old readiness accepted: %v", err)
	}
	fresh := scope(tenantA)
	fresh.RuntimeEpoch = nextEpoch
	if _, err := restarted.Resolve(ctx, fresh); !errors.Is(err, contract.ErrRuntimeNotReady) {
		t.Fatalf("restart implicitly registers: %v", err)
	}
	// This peer double sends only Auth-active entries. Auth owns the durable snapshot and retries.
	for _, entry := range []struct{ tenant, lifecycle string }{{tenantA, "active"}, {tenantB, "deleting"}} {
		target := scope(entry.tenant)
		target.RuntimeEpoch = nextEpoch
		if entry.lifecycle == "active" {
			ready, err := restarted.RegisterRuntime(ctx, target, registerOperation)
			if err != nil || ready.Epoch != nextEpoch || ready.LifecycleVersion != "1" || ready.State != "ready" {
				t.Fatalf("new readiness %+v %v", ready, err)
			}
		} else {
			target.LifecycleVersion = "2"
			if _, err := restarted.CloseRuntime(ctx, target, closeOperation); err != nil {
				t.Fatal(err)
			}
		}
	}
	empty, err := restarted.Resolve(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	products, err := empty.GetProducts(ctx)
	if err != nil || len(products.Items) != 0 || products.StateVersion != 0 {
		t.Fatalf("restored old state: %+v %v", products, err)
	}
	record, found, err := empty.FindRecordedCommand(ctx, "same")
	if err != nil || found || record.CommandID != "" {
		t.Fatalf("old outcome invented: %+v %v %v", record, found, err)
	}
	coverage, err := restarted.Coverage(ctx, fresh)
	if err != nil || !coverage.Equal(nextStart) || !coverage.After(oldCoverage) {
		t.Fatalf("new coverage %v %v", coverage, err)
	}
	deleted := scope(tenantB)
	deleted.RuntimeEpoch = nextEpoch
	if err := restarted.Register(ctx, deleted); !errors.Is(err, contract.ErrScopeDeleted) {
		t.Fatalf("Auth tombstone recreated: %v", err)
	}
}
func TestW2PartialCleanupCancellationAndRetry(t *testing.T) {
	var blocking atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	registry := newW2Registry(t, epoch, func() time.Time {
		if blocking.Load() {
			once.Do(func() { close(entered) })
			<-release
		}
		return now
	})
	ctx := context.Background()
	for _, tenant := range []string{tenantA, tenantB} {
		if err := registry.Register(ctx, scope(tenant)); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := registry.Resolve(ctx, scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	blocking.Store(true)
	commandDone := make(chan error, 1)
	go func() {
		_, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "admitted", ProductID: "P1", Name: "rice", BaseUnit: "g"})
		commandDone <- err
	}()
	<-entered
	closed := scope(tenantA)
	closed.LifecycleVersion = "2"
	timeout, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	pending, err := registry.CloseRuntime(timeout, closed, closeOperation)
	if !errors.Is(err, context.DeadlineExceeded) || pending.State != "pending" {
		t.Fatalf("partial cleanup %+v %v", pending, err)
	}
	if _, err := runtime.GetProducts(ctx); !errors.Is(err, contract.ErrScopeDeleted) {
		t.Fatalf("retained handle after cutoff: %v", err)
	}
	if _, err := registry.Resolve(ctx, scope(tenantA)); !errors.Is(err, contract.ErrScopeDeleted) {
		t.Fatalf("scope after cutoff: %v", err)
	}
	if err := registry.Register(ctx, scope(tenantA)); !errors.Is(err, contract.ErrScopeDeleted) {
		t.Fatalf("pending cleanup recreated: %v", err)
	}
	retryContext, retryCancel := context.WithCancel(ctx)
	type closeResult struct {
		readiness contract.RuntimeReadiness
		err       error
	}
	retryDone := make(chan closeResult, 1)
	go func() {
		readiness, err := registry.CloseRuntime(retryContext, closed, closeOperation)
		retryDone <- closeResult{readiness, err}
	}()
	select {
	case result := <-retryDone:
		t.Fatalf("cleanup acknowledged before admitted work finished: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	neighborDone := make(chan error, 1)
	go func() {
		_, err := registry.Resolve(ctx, scope(tenantB))
		neighborDone <- err
	}()
	select {
	case err := <-neighborDone:
		if err != nil {
			t.Fatalf("neighbor resolution: %v", err)
		}
	case <-time.After(time.Second):
		retryCancel()
		blocking.Store(false)
		close(release)
		t.Fatal("cleanup held the registry lock while it waited for admitted work")
	}
	retryCancel()
	result := <-retryDone
	if !errors.Is(result.err, context.Canceled) || result.readiness.State != "pending" {
		t.Fatalf("canceled retry lost pending cleanup: %+v", result)
	}
	blocking.Store(false)
	close(release)
	if err := <-commandDone; err != nil {
		t.Fatalf("previously admitted command changed outcome: %v", err)
	}
	ready, err := registry.CloseRuntime(ctx, closed, closeOperation)
	if err != nil || ready.State != "closed" {
		t.Fatalf("retry %+v %v", ready, err)
	}
	if _, err := runtime.FindOrderAttempt(ctx, "admitted", "proposal"); !errors.Is(err, contract.ErrScopeDeleted) {
		t.Fatalf("retained attempts: %v", err)
	}
}
func TestW2AllRetainedOperationsFenced(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	runtime, err := f.registry.Resolve(ctx, scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	closeScope := scope(tenantA)
	closeScope.LifecycleVersion = "2"
	if _, err := f.registry.CloseRuntime(ctx, closeScope, closeOperation); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"recover":     func() error { return runtime.Recover(ctx) },
		"product":     func() error { _, e := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{}); return e },
		"dish":        func() error { _, e := runtime.DefineDish(ctx, contract.DefineDishRequest{}); return e },
		"delivery":    func() error { _, e := runtime.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{}); return e },
		"evaluate":    func() error { _, e := runtime.EvaluateOrder(ctx, contract.EvaluateOrderRequest{}); return e },
		"menu":        func() error { _, e := runtime.EvaluateMenu(ctx); return e },
		"commit":      func() error { _, e := runtime.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{}); return e },
		"attempt":     func() error { _, e := runtime.FindOrderAttempt(ctx, "id", "proposal"); return e },
		"expirations": func() error { _, e := runtime.ProcessDueExpirations(ctx); return e },
		"expiryRun":   func() error { _, e := runtime.RunExpirations(ctx); return e },
		"expire":      func() error { _, e := runtime.ExpireBatch(ctx, contract.ExpireBatchRequest{}); return e },
		"inventory":   func() error { _, e := runtime.GetInventory(ctx); return e },
		"expiring":    func() error { _, e := runtime.GetExpiringBatches(ctx, now); return e },
		"history":     func() error { _, e := runtime.GetHistory(ctx); return e },
		"products":    func() error { _, e := runtime.GetProducts(ctx); return e },
		"catalog":     func() error { _, e := runtime.GetDishCatalog(ctx); return e },
		"dishDetails": func() error { _, e := runtime.GetDish(ctx, "id"); return e },
		"command":     func() error { _, _, e := runtime.FindRecordedCommand(ctx, "id"); return e },
		"order":       func() error { _, _, e := runtime.FindOrderByProposal(ctx, "id"); return e },
		"snapshot":    func() error { _, e := runtime.RunSnapshotOnce(ctx); return e },
		"worker":      func() error { return runtime.RunSnapshotWorker(ctx) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, contract.ErrScopeDeleted) {
				t.Fatalf("not fenced: %v", err)
			}
		})
	}
}
func TestW2CloseBeforeRegisterAndStaleClose(t *testing.T) {
	registry := newW2Registry(t, epoch, func() time.Time { return now })
	ctx := context.Background()
	target := scope(tenantA)
	target.LifecycleVersion = "2"
	if _, err := registry.CloseRuntime(ctx, target, closeOperation); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1", "2", "3"} {
		s := scope(tenantA)
		s.LifecycleVersion = version
		if err := registry.Register(ctx, s); !errors.Is(err, contract.ErrScopeDeleted) {
			t.Fatalf("generation %s: %v", version, err)
		}
	}
	active := scope(tenantB)
	active.LifecycleVersion = "3"
	if err := registry.Register(ctx, active); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1", "2", "3"} {
		s := active
		s.LifecycleVersion = version
		if _, err := registry.CloseRuntime(ctx, s, closeOperation); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("stale close %s: %v", version, err)
		}
	}
	if _, err := registry.Resolve(ctx, active); err != nil {
		t.Fatalf("stale close fenced live generation: %v", err)
	}
}
func TestW2SnapshotWorkersSurviveNeighborDeletion(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.registry.RunSnapshotWorker(ctx) }()
	target := scope(tenantA)
	target.LifecycleVersion = "2"
	timeout, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := f.registry.CloseRuntime(timeout, target, closeOperation); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("neighbor worker owner stopped on deletion: %v", err)
	default:
	}
	neighbor, err := f.registry.Resolve(ctx, scope(tenantB))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := neighbor.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "live", ProductID: "P1", Name: "rice", BaseUnit: "g"}); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("worker shutdown: %v", err)
		}
	case <-timeout.Done():
		t.Fatal("workers did not join")
	}
}
