package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestDirtyEmptyIndex(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	owner, _, clock := newSeededOwner(t, dueAt, 10000, 0)
	clock.Set(dueAt.Add(-time.Hour))
	if _, err := owner.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(owner.expirations) != 1 {
		t.Fatalf("initial expiration index size = %d; want 1", len(owner.expirations))
	}
	delete(owner.expirations, "batch")
	owner.usable = false
	clock.Set(dueAt)
	if _, err := owner.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := owner.state.Batches["batch"].RemainingQuantity; got != 0 {
		t.Fatalf("remaining after dirty empty-index recovery = %d; want 0", got)
	}
}

func TestProjectionFailureAfterCommit(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	appendCalls := 0
	wrapped := &eventStoreWrapper{EventStore: base, append: func(batch domain.CommandBatch, result domain.AppendResult, err error) (domain.AppendResult, error) {
		appendCalls++
		if err == nil && batch.CommandID == "expiration/batch" {
			result.Batch.Events[0].Sequence++
		}
		return result, err
	}}
	clock := &testClock{now: dueAt}
	owner, err := NewOwner(wrapped, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	request := domainExpiryRequest("expiration/batch", "batch")
	first, err := owner.ExpireBatch(context.Background(), request)
	if err != nil || first.WrittenOffQuantity != 10000 {
		t.Fatalf("first ExpireBatch() = %#v, %v; want committed result despite projection error", first, err)
	}
	if owner.usable {
		t.Fatal("projection stayed usable after applying malformed append result")
	}
	second, err := owner.ExpireBatch(context.Background(), request)
	if err != nil || second != first {
		t.Fatalf("replayed ExpireBatch() = %#v, %v; want the same recorded result", second, err)
	}
	if appendCalls != 1 {
		t.Fatalf("append calls = %d; want recorded result lookup before another write", appendCalls)
	}
	state, err := owner.Prepare(context.Background())
	if err != nil || state.State.Batches["batch"].RemainingQuantity != 0 {
		t.Fatalf("Prepare() after projection failure = %#v, %v; want recovered zero balance", state, err)
	}
}

func TestUnknownExpiryWritePreservesAttempt(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	appended := 0
	var original domain.CommandBatch
	wrapped := &eventStoreWrapper{EventStore: base, append: func(batch domain.CommandBatch, result domain.AppendResult, err error) (domain.AppendResult, error) {
		if batch.CommandID == "expiration/batch" {
			appended++
			original = *cloneCommandBatch(batch)
			return domain.AppendResult{}, domain.ErrUnknownWriteOutcome
		}
		return result, err
	}}
	clock := &testClock{now: dueAt}
	idCalls := 0
	owner, err := NewOwner(wrapped, clock.Now, func() string {
		idCalls++
		return "fixed-expiry-event"
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := owner.ProcessDueExpirations(context.Background())
	if err != nil || len(results) != 1 || results[0].WrittenOffQuantity != 10000 {
		t.Fatalf("ProcessDueExpirations() = %#v, %v; want resolved expiration", results, err)
	}
	if appended != 1 || idCalls != 1 {
		t.Fatalf("append calls/id calls = %d/%d; want one original attempt", appended, idCalls)
	}
	recorded, found, err := base.FindCommand(context.Background(), "expiration/batch")
	if err != nil || !found || recorded.Result.Expiry == nil {
		t.Fatalf("FindCommand() = %#v, %v, %v; want the recorded expiry", recorded, found, err)
	}
	if recorded.CommandFingerprint != domain.Fingerprint(original.CommandType, []byte("batch")) {
		t.Fatalf("recorded fingerprint = %q; want original attempt fingerprint", recorded.CommandFingerprint)
	}
	if recorded.Result.Expiry.BatchID != "batch" {
		t.Fatalf("recorded batchID = %q; want batch", recorded.Result.Expiry.BatchID)
	}
	batches, err := base.ReadBatches(context.Background(), 3)
	if err != nil || len(batches) != 1 || len(batches[0].Events) != 1 {
		t.Fatalf("ReadBatches() = %#v, %v; want one original complete expiry batch", batches, err)
	}
	event := batches[0].Events[0]
	if batches[0].FinalSequence != original.ExpectedVersion+1 || event.EventID != original.Events[0].EventID || event.Payload.Expiry == nil || *event.Payload.Expiry != *original.Events[0].Payload.Expiry {
		t.Fatalf("recorded attempt = %#v; want original sequence, event ID, and complete payload", batches[0])
	}
}

func TestCommandReadErrorIsNotAbsence(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	wrapped := &findCommandFailureStore{EventStore: base, err: errors.New("command read failed")}
	clock := &testClock{now: dueAt}
	owner, err := NewOwner(wrapped, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.ExpireBatch(context.Background(), domainExpiryRequest("expiration/batch", "batch"))
	if !errors.Is(err, contract.ErrInventoryUnavailable) {
		t.Fatalf("ExpireBatch() error = %v; want InventoryUnavailable on result lookup failure", err)
	}
	if version, err := base.Version(context.Background()); err != nil || version != 3 {
		t.Fatalf("version after failed lookup = %d, %v; want no appended event at version 3", version, err)
	}
}

func TestZeroBalanceExpirationIsEventless(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	owner, store, _ := newSeededOwner(t, dueAt, 10000, 10000)
	state, err := owner.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.State.Batches["batch"].RemainingQuantity != 0 || state.StateVersion != 4 {
		t.Fatalf("state = version %d, remaining %d; want already-consumed zero at version 4", state.StateVersion, state.State.Batches["batch"].RemainingQuantity)
	}
	if _, found, err := store.FindCommand(context.Background(), "expiration/batch"); err != nil || found {
		t.Fatalf("FindCommand(expiration/batch) = found %v, err %v; want no zero-write-off command", found, err)
	}
}

func TestPendingUnknownWriteClearsAfterVersionConflict(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	wrapped := &unknownConflictStore{EventStore: base, store: base}
	clock := &testClock{now: dueAt}
	owner, err := NewOwner(wrapped, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	state, err := owner.Prepare(context.Background())
	if err != nil {
		t.Fatalf("Prepare() after exact retry conflict: %v", err)
	}
	if state.StateVersion != 5 || state.State.Batches["batch"].RemainingQuantity != 0 {
		t.Fatalf("state = version %d, remaining %d; want external command plus refreshed expiry", state.StateVersion, state.State.Batches["batch"].RemainingQuantity)
	}
	if _, found := state.State.Products["external-product"]; !found {
		t.Fatal("recovery lost the external command that caused the version conflict")
	}
	if wrapped.expiryAttempts != 3 || owner.pending != nil {
		t.Fatalf("expiry attempts/pending = %d/%v; want fresh attempt and no obsolete pending batch", wrapped.expiryAttempts, owner.pending)
	}
}

func TestUnknownCommittedExpiryReturnsWhenProjectionReadFails(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	wrapped := &unknownCommitReadFailureStore{EventStore: base, failAfter: 3}
	clock := &testClock{now: dueAt}
	owner, err := NewOwner(wrapped, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.ExpireBatch(context.Background(), expireRequest("batch"))
	if err != nil || result.WrittenOffQuantity != 10000 {
		t.Fatalf("ExpireBatch() = %#v, %v; want recorded expiry despite projection read error", result, err)
	}
	if owner.usable || owner.pending != nil || wrapped.expiryAppends != 1 {
		t.Fatalf("projection usable/pending/appends = %v/%v/%d; want dirty projection, resolved write, and one append", owner.usable, owner.pending, wrapped.expiryAppends)
	}
	state, err := owner.Prepare(context.Background())
	if err != nil || state.State.Batches["batch"].RemainingQuantity != 0 {
		t.Fatalf("Prepare() recovery = %#v, %v; want recovered committed expiry", state, err)
	}
}

type unknownConflictStore struct {
	EventStore
	store          *memory.EventStore
	expiryAttempts int
}

func (s *unknownConflictStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	if batch.CommandID != "expiration/batch" {
		return s.EventStore.AppendBatch(ctx, batch)
	}
	s.expiryAttempts++
	if s.expiryAttempts == 1 {
		return domain.AppendResult{}, domain.ErrUnknownWriteOutcome
	}
	if s.expiryAttempts == 2 {
		at := moscowTime(2026, time.April, 1, 10)
		if err := appendFixtureWithoutTest(s.store, ctx, "external-register", "RegisterProduct", domain.Event{EventID: "external-register-event", RecordedAt: at, Type: domain.EventProductRegistered, Payload: domain.EventPayload{Product: &domain.Product{ProductID: "external-product", Name: "External", BaseUnit: domain.BaseUnitG}}}); err != nil {
			return domain.AppendResult{}, err
		}
	}
	return s.EventStore.AppendBatch(ctx, batch)
}

func appendFixtureWithoutTest(store *memory.EventStore, ctx context.Context, commandID, commandType string, event domain.Event) error {
	version, err := store.Version(ctx)
	if err != nil {
		return err
	}
	_, err = store.AppendBatch(ctx, domain.CommandBatch{ExpectedVersion: version, CommandID: commandID, CommandType: commandType, CommandFingerprint: domain.Fingerprint(commandType, []byte(commandID)), Events: []domain.Event{event}})
	return err
}

type unknownCommitReadFailureStore struct {
	EventStore
	failAfter     int64
	failed        bool
	expiryAppends int
}

func (s *unknownCommitReadFailureStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	result, err := s.EventStore.AppendBatch(ctx, batch)
	if batch.CommandID == "expiration/batch" {
		s.expiryAppends++
		if s.expiryAppends == 1 && err == nil {
			return domain.AppendResult{}, domain.ErrUnknownWriteOutcome
		}
	}
	return result, err
}

func (s *unknownCommitReadFailureStore) ReadBatches(ctx context.Context, afterSequence int64) ([]domain.RecordedBatch, error) {
	if !s.failed && afterSequence == s.failAfter {
		s.failed = true
		return nil, errors.New("projection read failed")
	}
	return s.EventStore.ReadBatches(ctx, afterSequence)
}

func (s *unknownCommitReadFailureStore) ScanBatches(ctx context.Context, afterSequence int64, fn func(domain.RecordedBatch) error) error {
	return scanViaRead(ctx, s, afterSequence, fn)
}

type findCommandFailureStore struct {
	EventStore
	err error
}

func (s *findCommandFailureStore) FindCommand(context.Context, string) (domain.CommandRecord, bool, error) {
	return domain.CommandRecord{}, false, s.err
}

func domainExpiryRequest(commandID, batchID string) contract.ExpireBatchRequest {
	return contract.ExpireBatchRequest{CommandID: commandID, BatchID: batchID}
}
