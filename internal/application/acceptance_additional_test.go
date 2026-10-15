package application

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestAcceptanceExpiryWriteFailureRetainsTaskAndRetries(t *testing.T) {
	ctx := context.Background()
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	wrapped := &acceptanceFailBeforeAppendStore{EventStore: base, commandID: "expiration/batch", failures: 1}
	clock := &testClock{now: dueAt}
	owner, err := NewOwner(wrapped, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Prepare(ctx); !errors.Is(err, contract.ErrInventoryUnavailable) {
		t.Fatalf("Prepare() on pre-commit expiry failure = %v; want InventoryUnavailable", err)
	}
	if version, err := base.Version(ctx); err != nil || version != 3 {
		t.Fatalf("version after failed expiry = %d, %v; want unchanged version 3", version, err)
	}
	clock.Set(dueAt.Add(-time.Minute))
	view, err := owner.GetInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.StateVersion != 3 || len(view.Products) != 1 || view.Products[0].Quantity != 10000 {
		t.Fatalf("inventory after failed expiry = %#v; want original 10000 balance at version 3", view)
	}
	clock.Set(dueAt)
	results, err := owner.ProcessDueExpirations(ctx)
	if err != nil || len(results) != 1 || results[0].BatchID != "batch" || results[0].WrittenOffQuantity != 10000 {
		t.Fatalf("retry ProcessDueExpirations() = %#v, %v; want one 10000 write-off", results, err)
	}
	if version, err := base.Version(ctx); err != nil || version != 4 {
		t.Fatalf("version after successful retry = %d, %v; want exactly one expiry event", version, err)
	}
	if _, found, err := base.FindCommand(ctx, "expiration/batch"); err != nil || !found {
		t.Fatalf("expiry command after retry found=%v err=%v; want recorded command", found, err)
	}
}

func TestAcceptanceExpiryFailurePreventsAcceptedOrder(t *testing.T) {
	ctx := context.Background()
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	wrapped := &acceptanceFailBeforeAppendStore{EventStore: base, commandID: "expiration/batch", failures: 1}
	owner, err := NewOwner(wrapped, func() time.Time { return dueAt }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	request := contract.CommitOrderRequest{CommandID: "accepted-order", ProposalID: "accepted-proposal", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}}
	if _, err := owner.CommitAcceptedOrder(ctx, request); !errors.Is(err, contract.ErrInventoryUnavailable) {
		t.Fatalf("CommitAcceptedOrder() on mandatory expiry failure = %v; want InventoryUnavailable", err)
	}
	history, err := owner.GetHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if history.StateVersion != 3 || len(history.Events) != 3 {
		t.Fatalf("history after blocked order = version %d with %d events; want unchanged version 3", history.StateVersion, len(history.Events))
	}
	for _, event := range history.Events {
		if event.Type == contract.EventOrderPlaced || event.Type == contract.EventBatchExpired {
			t.Fatalf("event %s was written after mandatory expiry failure; want no expiry or order/debit", event.Type)
		}
	}
	if _, found, err := base.FindCommand(ctx, request.CommandID); err != nil || found {
		t.Fatalf("order command found=%v err=%v after blocked commit; want no ConfirmOrder", found, err)
	}
}

func TestAcceptanceFailedReconciliationBlocksAdmissionAndRepairs(t *testing.T) {
	ctx := context.Background()
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	wrapped := &acceptanceFailFullReadStore{EventStore: base}
	clock := &testClock{now: dueAt.Add(-time.Hour)}
	owner, err := NewOwner(wrapped, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	initial, err := owner.GetInventory(ctx)
	if err != nil || initial.Products[0].Quantity != 10000 {
		t.Fatalf("initial GetInventory() = %#v, %v; want 10000 balance", initial, err)
	}
	assertOldProjection := func() {
		t.Helper()
		owner.mu.Lock()
		defer owner.mu.Unlock()
		if owner.state.StateVersion != 3 || owner.state.Batches["batch"].RemainingQuantity != 10000 {
			t.Fatalf("failed reconciliation changed old inventory: %#v", owner.state)
		}
		if len(owner.expirations) != 1 || !owner.expirations["batch"].Equal(dueAt) {
			t.Fatalf("failed reconciliation changed old expiration index: %#v", owner.expirations)
		}
	}
	wrapped.failNextFullReads(1)
	if err := owner.Recover(ctx); !errors.Is(err, contract.ErrInventoryUnavailable) {
		t.Fatalf("Recover() during rebuild failure = %v; want InventoryUnavailable", err)
	}
	assertOldProjection()
	owner.mu.Lock()
	if !owner.usable {
		owner.mu.Unlock()
		t.Fatal("a failed rebuild made a consistent projection unusable")
	}
	// An inconsistency found elsewhere marks the projection unusable.
	owner.usable = false
	owner.mu.Unlock()
	wrapped.failNextFullReads(1)
	clock.Set(dueAt)
	if _, err := owner.GetInventory(ctx); !errors.Is(err, contract.ErrInventoryUnavailable) {
		t.Fatalf("GetInventory() while rebuild still fails = %v; want InventoryUnavailable", err)
	}
	assertOldProjection()
	if version, err := base.Version(ctx); err != nil || version != 3 {
		t.Fatalf("version while reconciliation is unavailable = %d, %v; want no expiry/order write", version, err)
	}
	wrapped.clearFailures()
	view, err := owner.GetInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.StateVersion != 4 || len(view.Products) != 1 || view.Products[0].Quantity != 0 || len(view.Products[0].Batches) != 0 {
		t.Fatalf("inventory after reconciliation repair = %#v; want repaired zero balance at version 4", view)
	}
	if _, found, err := base.FindCommand(ctx, "expiration/batch"); err != nil || !found {
		t.Fatalf("expiry command after repaired reconciliation found=%v err=%v; want recorded expiry", found, err)
	}
}

func TestAcceptanceStaleReconciliationCannotRestoreSpentBatch(t *testing.T) {
	ctx := context.Background()
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	staleStore := &acceptanceGateFullReadStore{EventStore: base, entered: make(chan struct{}), release: make(chan struct{})}
	clockA := &testClock{now: dueAt.Add(-time.Minute)}
	ownerA, err := NewOwner(staleStore, clockA.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerA.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	staleStore.arm()
	recovered := make(chan error, 1)
	go func() { recovered <- ownerA.Recover(ctx) }()
	<-staleStore.entered
	ownerB, err := NewOwner(base, func() time.Time { return dueAt }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	result, err := ownerB.ExpireBatch(ctx, domainExpiryRequest("expiration/batch", "batch"))
	if err != nil || result.WrittenOffQuantity != 10000 {
		t.Fatalf("concurrent owner expiry = %#v, %v; want one 10000 write-off", result, err)
	}
	close(staleStore.release)
	if err := <-recovered; !errors.Is(err, contract.ErrInventoryUnavailable) {
		t.Fatalf("stale Recover() = %v; want InventoryUnavailable after journal advanced", err)
	}
	view, err := ownerA.GetInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.StateVersion != 4 || len(view.Products) != 1 || view.Products[0].Quantity != 0 || len(view.Products[0].Batches) != 0 {
		t.Fatalf("repaired inventory after concurrent spend = %#v; want no stock or due task", view)
	}
	if _, found, err := base.FindCommand(ctx, "expiration/batch"); err != nil || !found {
		t.Fatalf("expiration command found=%v err=%v; want one committed spend", found, err)
	}
	if version, err := base.Version(ctx); err != nil || version != 4 {
		t.Fatalf("version after stale reconciliation = %d, %v; want no duplicate expiry", version, err)
	}
}

func TestAcceptanceReaderSeesWholeDeliveryAndExpiryProjection(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	base := memory.NewEventStore()
	clock := &testClock{now: now}
	appendGate := &acceptanceGateAppendStore{EventStore: base, commandType: "ReceiveDelivery", entered: make(chan struct{}), release: make(chan struct{})}
	owner, err := NewOwner(appendGate, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	delivery := deliveryRequest("expired-delivery", "expired-delivery-id", now, []contract.DeliveryLine{{ProductID: "p", ManufacturedAt: now.Add(-24 * time.Hour), ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}, ReceivedAt: now, Quantity: 10000}})
	deliveryDone := make(chan error, 1)
	go func() {
		_, err := owner.ReceiveDelivery(ctx, delivery)
		deliveryDone <- err
	}()
	<-appendGate.entered
	readerAtGate := make(chan bool, 1)
	readerDone := make(chan struct {
		view contract.InventoryView
		err  error
	}, 1)
	go func() {
		entered := owner.mu.TryLock()
		if entered {
			owner.mu.Unlock()
		}
		readerAtGate <- entered
		view, err := owner.GetInventory(ctx)
		readerDone <- struct {
			view contract.InventoryView
			err  error
		}{view: view, err: err}
	}()
	if <-readerAtGate {
		close(appendGate.release)
		<-deliveryDone
		<-readerDone
		t.Fatal("reader entered the owner gate while delivery append was paused")
	}
	if owner.state.StateVersion != 1 || len(owner.state.Batches) != 0 || len(owner.expirations) != 0 {
		close(appendGate.release)
		<-deliveryDone
		<-readerDone
		t.Fatal("delivery published an intermediate inventory or expiration index")
	}
	close(appendGate.release)
	if err := <-deliveryDone; err != nil {
		t.Fatal(err)
	}
	read := <-readerDone
	if read.err != nil {
		t.Fatal(read.err)
	}
	if read.view.StateVersion != 3 || len(read.view.Products) != 1 || read.view.Products[0].Quantity != 0 || len(read.view.Products[0].Batches) != 0 {
		t.Fatalf("reader inventory after atomic delivery+expiry = %#v; want final zero-stock state at version 3", read.view)
	}
	owner.mu.Lock()
	version, tasks := owner.state.StateVersion, len(owner.expirations)
	owner.mu.Unlock()
	if version != 3 || tasks != 0 {
		t.Fatalf("completed projection version=%d expiration tasks=%d; want 3 and 0", version, tasks)
	}
	history, err := owner.GetHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Events) != 3 || history.Events[1].Type != contract.EventDeliveryReceived || history.Events[2].Type != contract.EventBatchExpired {
		t.Fatalf("delivery command history = %#v; want delivery and expiry committed as one two-event command", history.Events)
	}
}

func TestAcceptanceDeliveryOverflowRechecksAfterConcurrentModification(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	base := memory.NewEventStore()
	setup := newOwnerForCatalogTest(t, base, now)
	if _, err := setup.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "register-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	gate := &acceptanceGateAppendStore{EventStore: base, commandType: "ReceiveDelivery", entered: make(chan struct{}), release: make(chan struct{})}
	ownerA, err := NewOwner(gate, func() time.Time { return now }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	ownerB, err := NewOwner(base, func() time.Time { return now }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	requestA := validDeliveryRequest("delivery-small", "delivery-small", now)
	requestA.Lines[0].Quantity = 1
	firstResult := make(chan error, 1)
	go func() {
		_, err := ownerA.ReceiveDelivery(ctx, requestA)
		firstResult <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("small delivery did not reach the controlled append boundary")
	}

	requestB := validDeliveryRequest("delivery-max", "delivery-max", now)
	requestB.Lines[0].Quantity = contract.Quantity(math.MaxInt64)
	if _, err := ownerB.ReceiveDelivery(ctx, requestB); err != nil {
		t.Fatalf("concurrent MaxInt64 delivery error = %v", err)
	}
	close(gate.release)
	if err := <-firstResult; !errors.Is(err, domain.ErrQuantityOverflow) {
		t.Fatalf("small delivery after concurrent MaxInt64 write error = %v; want refreshed quantity overflow", err)
	}
	if _, found, err := base.FindCommand(ctx, requestA.CommandID); err != nil || found {
		t.Fatalf("stale-version delivery command found=%v err=%v; want no receipt facts", found, err)
	}
	if _, found, err := base.FindCommand(ctx, requestB.CommandID); err != nil || !found {
		t.Fatalf("concurrent winning delivery found=%v err=%v; want recorded command", found, err)
	}
	history, err := ownerA.GetHistory(ctx)
	if err != nil || history.StateVersion != 2 || len(history.Events) != 2 || history.Events[1].CommandID != requestB.CommandID {
		t.Fatalf("history after stale candidate rejection = version %d events %#v error %v; want only concurrent delivery", history.StateVersion, history.Events, err)
	}
}

func TestAcceptanceInvalidDeliveryTimesUseIndependentLineSlices(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, store, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	base := validDeliveryRequest("unused", "unused", moscowTime(2026, time.April, 2, 9))
	requests := make([]contract.ReceiveDeliveryRequest, 4)
	for index := range requests {
		requests[index] = base
		requests[index].Lines = append([]contract.DeliveryLine(nil), base.Lines...)
		requests[index].CommandID = []string{"invalid-created-at", "invalid-manufactured-at", "invalid-received-at", "invalid-offset"}[index]
	}
	requests[0].CreatedAt = now.Add(time.Minute)
	requests[1].Lines[0].ManufacturedAt = requests[1].Lines[0].ReceivedAt.Add(time.Minute)
	requests[2].Lines[0].ReceivedAt = now.Add(time.Minute)
	requests[3].Lines[0].ReceivedAt = requests[3].Lines[0].ReceivedAt.In(time.FixedZone("UTC", 0))
	for index, request := range requests {
		if _, err := owner.ReceiveDelivery(ctx, request); !errors.Is(err, contract.ErrInvalidArgument) {
			t.Fatalf("ReceiveDelivery(invalid time case %d) = %v; want InvalidArgument", index, err)
		}
	}
	if version, err := store.Version(ctx); err != nil || version != 1 {
		t.Fatalf("version after independent invalid time cases = %d, %v; want only product event", version, err)
	}
}

type acceptanceFailBeforeAppendStore struct {
	EventStore
	commandID string
	failures  int
}

func (s *acceptanceFailBeforeAppendStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	if batch.CommandID == s.commandID && s.failures > 0 {
		s.failures--
		return domain.AppendResult{}, errors.New("acceptance injected pre-commit append failure")
	}
	return s.EventStore.AppendBatch(ctx, batch)
}

type acceptanceFailFullReadStore struct {
	EventStore
	mu       sync.Mutex
	failures int
}

func (s *acceptanceFailFullReadStore) failNextFullReads(count int) {
	s.mu.Lock()
	s.failures = count
	s.mu.Unlock()
}

func (s *acceptanceFailFullReadStore) clearFailures() {
	s.failNextFullReads(0)
}

func (s *acceptanceFailFullReadStore) ReadBatches(ctx context.Context, afterSequence int64) ([]domain.RecordedBatch, error) {
	if afterSequence == 0 {
		s.mu.Lock()
		if s.failures > 0 {
			s.failures--
			s.mu.Unlock()
			return nil, errors.New("acceptance injected reconciliation failure")
		}
		s.mu.Unlock()
	}
	return s.EventStore.ReadBatches(ctx, afterSequence)
}

func (s *acceptanceFailFullReadStore) ScanBatches(ctx context.Context, afterSequence int64, fn func(domain.RecordedBatch) error) error {
	return scanViaRead(ctx, s, afterSequence, fn)
}

type acceptanceGateFullReadStore struct {
	EventStore
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func (s *acceptanceGateFullReadStore) arm() {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
}

func (s *acceptanceGateFullReadStore) ReadBatches(ctx context.Context, afterSequence int64) ([]domain.RecordedBatch, error) {
	batches, err := s.EventStore.ReadBatches(ctx, afterSequence)
	if afterSequence != 0 {
		return batches, err
	}
	s.mu.Lock()
	armed := s.armed
	s.armed = false
	s.mu.Unlock()
	if armed {
		close(s.entered)
		<-s.release
	}
	return batches, err
}

func (s *acceptanceGateFullReadStore) ScanBatches(ctx context.Context, afterSequence int64, fn func(domain.RecordedBatch) error) error {
	return scanViaRead(ctx, s, afterSequence, fn)
}

type acceptanceGateAppendStore struct {
	EventStore
	commandType string
	once        sync.Once
	entered     chan struct{}
	release     chan struct{}
}

func (s *acceptanceGateAppendStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	if batch.CommandType == s.commandType {
		s.once.Do(func() {
			close(s.entered)
			<-s.release
		})
	}
	return s.EventStore.AppendBatch(ctx, batch)
}
