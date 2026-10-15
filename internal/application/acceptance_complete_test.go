package application

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestAcceptanceSeparateArrivalsKeepIndependentFactsAndStock(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, _, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "register-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "define-d", DishID: "d", Name: "Pear dish", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 1000}}}); err != nil {
		t.Fatal(err)
	}
	first := deliveryRequest("receive-one", "delivery-one", now, []contract.DeliveryLine{{ProductID: "p", ManufacturedAt: now.Add(-24 * time.Hour), ShelfLife: contract.ShelfLife{Value: 3, Unit: contract.ShelfLifeDay}, ReceivedAt: now, Quantity: 1000}})
	second := deliveryRequest("receive-two", "delivery-two", now, []contract.DeliveryLine{{ProductID: "p", ManufacturedAt: now.Add(-48 * time.Hour), ShelfLife: contract.ShelfLife{Value: 5, Unit: contract.ShelfLifeDay}, ReceivedAt: now.Add(-time.Hour), Quantity: 1000}})
	firstReceipt, err := owner.ReceiveDelivery(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	secondReceipt, err := owner.ReceiveDelivery(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstReceipt.BatchIDs) != 1 || len(secondReceipt.BatchIDs) != 1 || firstReceipt.BatchIDs[0] == secondReceipt.BatchIDs[0] {
		t.Fatalf("arrival batch IDs = %#v and %#v; want two distinct batches", firstReceipt.BatchIDs, secondReceipt.BatchIDs)
	}
	a, b := owner.state.Batches[firstReceipt.BatchIDs[0]], owner.state.Batches[secondReceipt.BatchIDs[0]]
	if a.DeliveryID != "delivery-one" || !a.ReceivedAt.Equal(now) || !a.ExpiresAt.Equal(now.Add(48*time.Hour)) || b.DeliveryID != "delivery-two" || !b.ReceivedAt.Equal(now.Add(-time.Hour)) || !b.ExpiresAt.Equal(now.Add(72*time.Hour)) {
		t.Fatalf("arrival metadata = %#v and %#v; want independent delivery, received, and expiry facts", a, b)
	}
	evaluation, err := owner.EvaluateOrder(ctx, contract.EvaluateOrderRequest{Positions: []contract.Position{{LineNo: 1, DishID: "d", Portions: 2}}})
	if err != nil || len(evaluation.AcceptedPositions) != 1 || evaluation.AcceptedPositions[0].Portions != 2 {
		t.Fatalf("EvaluateOrder() = %#v, %v; want both received portions available", evaluation, err)
	}
	result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "order-two-arrivals", ProposalID: "proposal-two-arrivals", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 2}}})
	if err != nil || result.Order == nil || !reflect.DeepEqual(result.Order.Allocations, []contract.Allocation{{ProductID: "p", BatchID: a.BatchID, Quantity: 1000}, {ProductID: "p", BatchID: b.BatchID, Quantity: 1000}}) {
		t.Fatalf("CommitAcceptedOrder() allocations = %#v, %v; want both arrival batches in FEFO order", result.Order, err)
	}
}

func TestAcceptanceBeforeAndAtExpiryBoundary(t *testing.T) {
	ctx := context.Background()
	dueAt := moscowTime(2026, time.April, 2, 10)
	owner, store, clock := newSeededOwner(t, dueAt, 10000, 0)
	clock.Set(dueAt.Add(-time.Nanosecond))
	before, err := owner.Prepare(ctx)
	if err != nil || before.State.Batches["batch"].RemainingQuantity != 10000 || before.StateVersion != 3 {
		t.Fatalf("Prepare() just before expiry = %#v, %v; want 10000 at version 3", before, err)
	}
	if _, found, err := store.FindCommand(ctx, "expiration/batch"); err != nil || found {
		t.Fatalf("expiry before boundary found=%v err=%v; want no event", found, err)
	}
	clock.Set(dueAt)
	equal, err := owner.Prepare(ctx)
	if err != nil || equal.State.Batches["batch"].RemainingQuantity != 0 || equal.State.Batches["batch"].WrittenOffForExpiry != 10000 || equal.StateVersion != 4 {
		t.Fatalf("Prepare() at expiry = %#v, %v; want full 10000 write-off at version 4", equal, err)
	}
}

func TestAcceptanceHalfGramRecipeUsesExactFEFOQuantities(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, _, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "register-p", ProductID: "p", Name: "Salt", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "define-d", DishID: "d", Name: "Dish", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 500}}}); err != nil {
		t.Fatal(err)
	}
	for _, delivery := range []struct {
		command string
		id      string
		qty     contract.Quantity
		madeAt  time.Time
		life    int64
	}{{"receive-first", "delivery-first", 750, now.Add(-24 * time.Hour), 3}, {"receive-second", "delivery-second", 500, now.Add(-48 * time.Hour), 5}} {
		request := deliveryRequest(delivery.command, delivery.id, now, []contract.DeliveryLine{{ProductID: "p", ManufacturedAt: delivery.madeAt, ShelfLife: contract.ShelfLife{Value: delivery.life, Unit: contract.ShelfLifeDay}, ReceivedAt: now, Quantity: delivery.qty}})
		if _, err := owner.ReceiveDelivery(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	evaluation, err := owner.EvaluateOrder(ctx, contract.EvaluateOrderRequest{Positions: []contract.Position{{LineNo: 1, DishID: "d", Portions: 3}}})
	if err != nil || len(evaluation.AcceptedPositions) != 1 || evaluation.AcceptedPositions[0].Portions != 2 || len(evaluation.Changes) != 1 || evaluation.Changes[0].Deficits[0].RequiredQuantity != 1500 || evaluation.Changes[0].Deficits[0].AvailableQuantity != 1250 {
		t.Fatalf("three-portion evaluation = %#v, %v; want exactly two portions from 1250 units of 500 each", evaluation, err)
	}
	result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "half-gram-order", ProposalID: "half-gram-proposal", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 2}}})
	want := []contract.Allocation{{ProductID: "p", BatchID: "owner-event-3", Quantity: 750}, {ProductID: "p", BatchID: "owner-event-5", Quantity: 250}}
	if err != nil || result.Order == nil || !reflect.DeepEqual(result.Order.Allocations, want) {
		t.Fatalf("half-gram allocations = %#v, %v; want exact FEFO split %#v", result.Order, err, want)
	}
	if owner.state.Batches["owner-event-5"].RemainingQuantity != 250 {
		t.Fatalf("later batch remainder = %d; want exact 250 units", owner.state.Batches["owner-event-5"].RemainingQuantity)
	}
}

func TestAcceptanceConcurrentDuplicateDeliverySurvivesRecovery(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, store, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "register-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	base := validDeliveryRequest("delivery-command-a", "shared-delivery", now)
	other := base
	other.CommandID = "delivery-command-b"
	type outcome struct {
		command string
		err     error
	}
	results := make(chan outcome, 2)
	for _, request := range []contract.ReceiveDeliveryRequest{base, other} {
		request := request
		go func() {
			_, err := owner.ReceiveDelivery(ctx, request)
			results <- outcome{command: request.CommandID, err: err}
		}()
	}
	var accepted, duplicate string
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			accepted = result.command
		case errors.Is(result.err, contract.ErrDeliveryAlreadyReceived):
			duplicate = result.command
		default:
			t.Fatalf("concurrent command %q error = %v; want one success and one duplicate", result.command, result.err)
		}
	}
	if accepted == "" || duplicate == "" {
		t.Fatalf("accepted/duplicate commands = %q/%q; want exactly one of each", accepted, duplicate)
	}
	versionBefore, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := NewOwner(store, func() time.Time { return now }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	retry := base
	if duplicate == other.CommandID {
		retry = other
	}
	if _, err := recovered.ReceiveDelivery(ctx, retry); !errors.Is(err, contract.ErrDeliveryAlreadyReceived) {
		t.Fatalf("recovered duplicate command error = %v; want DeliveryAlreadyReceived", err)
	}
	versionAfter, err := store.Version(ctx)
	if err != nil || versionAfter != versionBefore || len(recovered.state.DeliveryIDs) != 1 || len(recovered.state.Batches) != 1 || recovered.state.Batches["owner-event-2"].RemainingQuantity != 1000 {
		t.Fatalf("post-recovery duplicate version/state = %d, %#v, %v; want one unchanged delivery and stock", versionAfter, recovered.state, err)
	}
}

func TestAcceptanceConcurrentDuplicateDeliveryAcrossOwners(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	setup, base, _ := newCatalogOwner(t, now)
	if _, err := setup.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "register-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	barrier := &acceptanceConcurrentAppendBarrierStore{EventStore: base, arrivals: make(chan domain.CommandBatch, 2), release: make(chan struct{}), seen: make(map[string]bool)}
	ownerA, err := NewOwner(barrier, func() time.Time { return now }, prefixedAcceptanceID("owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	ownerB, err := NewOwner(barrier, func() time.Time { return now }, prefixedAcceptanceID("owner-b"))
	if err != nil {
		t.Fatal(err)
	}
	requestA := validDeliveryRequest("cross-owner-command-a", "shared-delivery", now)
	requestB := requestA
	requestB.CommandID = "cross-owner-command-b"
	type outcome struct {
		command string
		err     error
	}
	results := make(chan outcome, 2)
	go func() {
		_, err := ownerA.ReceiveDelivery(ctx, requestA)
		results <- outcome{command: requestA.CommandID, err: err}
	}()
	go func() {
		_, err := ownerB.ReceiveDelivery(ctx, requestB)
		results <- outcome{command: requestB.CommandID, err: err}
	}()
	releaseBoth := sync.OnceFunc(func() { close(barrier.release) })
	defer releaseBoth()
	first := receiveAcceptanceAppend(t, barrier.arrivals)
	second := receiveAcceptanceAppend(t, barrier.arrivals)
	if first.ExpectedVersion != second.ExpectedVersion || first.ExpectedVersion != 1 || first.CommandID == second.CommandID {
		t.Fatalf("first append attempts = (%s, version %d), (%s, version %d); want distinct commands at shared version 1", first.CommandID, first.ExpectedVersion, second.CommandID, second.ExpectedVersion)
	}
	releaseBoth()
	var accepted, duplicate string
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			accepted = result.command
		case errors.Is(result.err, contract.ErrDeliveryAlreadyReceived):
			duplicate = result.command
		default:
			t.Fatalf("cross-owner command %q error = %v; want one success and one DeliveryAlreadyReceived", result.command, result.err)
		}
	}
	if accepted == "" || duplicate == "" {
		t.Fatalf("cross-owner accepted/duplicate commands = %q/%q; want one each", accepted, duplicate)
	}
	barrier.mu.Lock()
	conflicts := barrier.conflicts
	barrier.mu.Unlock()
	if conflicts != 1 {
		t.Fatalf("shared EventStore concurrent-modification results = %d; want exactly one conflict", conflicts)
	}
	versionBeforeRetry, err := base.Version(ctx)
	if err != nil || versionBeforeRetry != 2 {
		t.Fatalf("journal version after cross-owner race = %d, %v; want one delivery append after registration", versionBeforeRetry, err)
	}
	batches, err := base.ReadBatches(ctx, 0)
	if err != nil || len(batches) != 2 || batches[1].CommandType != "ReceiveDelivery" || len(batches[1].Events) != 1 {
		t.Fatalf("journal after cross-owner race = %#v, %v; want registration and one receipt batch", batches, err)
	}
	recovered, err := NewOwner(base, func() time.Time { return now }, prefixedAcceptanceID("owner-recovered"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	loser := requestA
	if duplicate == requestB.CommandID {
		loser = requestB
	}
	if _, err := recovered.ReceiveDelivery(ctx, loser); !errors.Is(err, contract.ErrDeliveryAlreadyReceived) {
		t.Fatalf("recovered losing command retry = %v; want DeliveryAlreadyReceived", err)
	}
	versionAfterRetry, err := base.Version(ctx)
	if err != nil || versionAfterRetry != versionBeforeRetry || len(recovered.state.DeliveryIDs) != 1 || len(recovered.state.Batches) != 1 {
		t.Fatalf("journal/projection after recovered loser retry = version %d, deliveries %d, batches %d, err %v; want unchanged single delivery", versionAfterRetry, len(recovered.state.DeliveryIDs), len(recovered.state.Batches), err)
	}
	for batchID, batch := range recovered.state.Batches {
		if batch.RemainingQuantity != 1000 {
			t.Fatalf("recovered stock for %s = %d; want one 1000-unit delivery without growth", batchID, batch.RemainingQuantity)
		}
	}
}

func receiveAcceptanceAppend(t *testing.T, arrivals <-chan domain.CommandBatch) domain.CommandBatch {
	t.Helper()
	select {
	case batch := <-arrivals:
		return batch
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent ReceiveDelivery did not reach the append barrier")
		return domain.CommandBatch{}
	}
}

func TestAcceptanceConsumedBatchLeavesNoExpiryTask(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	owner, store, _ := newSeededOwner(t, dueAt, 10000, 10000)
	state, err := owner.Prepare(context.Background())
	if err != nil || state.State.Batches["batch"].RemainingQuantity != 0 || len(owner.expirations) != 0 {
		t.Fatalf("fully consumed batch state/index = %#v/%#v, %v; want zero balance and no task", state, owner.expirations, err)
	}
	if _, found, err := store.FindCommand(context.Background(), "expiration/batch"); err != nil || found {
		t.Fatalf("expiration command found=%v err=%v; want no zero-quantity expiry", found, err)
	}
}

func TestAcceptancePartialExpiryFailureBlocksOrderAndRetryFinishes(t *testing.T) {
	ctx := context.Background()
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	appendSecondBatch(t, base, dueAt, 7000)
	wrapper := &acceptanceFailBeforeAppendStore{EventStore: base, commandID: "expiration/batch-2", failures: 1}
	owner, err := NewOwner(wrapper, func() time.Time { return dueAt }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	request := contract.CommitOrderRequest{CommandID: "after-two-expiries", ProposalID: "after-two-expiries", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}}
	if _, err := owner.CommitAcceptedOrder(ctx, request); !errors.Is(err, contract.ErrInventoryUnavailable) {
		t.Fatalf("CommitAcceptedOrder() after second expiry write failure = %v; want InventoryUnavailable", err)
	}
	if owner.state.Batches["batch"].RemainingQuantity != 0 || owner.state.Batches["batch-2"].RemainingQuantity != 7000 {
		t.Fatalf("balances after partial failure = %d/%d; want first write retained and second untouched", owner.state.Batches["batch"].RemainingQuantity, owner.state.Batches["batch-2"].RemainingQuantity)
	}
	if _, found, err := base.FindCommand(ctx, request.CommandID); err != nil || found {
		t.Fatalf("order after partial expiry failure found=%v err=%v; want no ConfirmOrder", found, err)
	}
	retry, err := owner.CommitAcceptedOrder(ctx, request)
	if err != nil || retry.Kind != contract.CommitResultRecheckRequired {
		t.Fatalf("CommitAcceptedOrder() after expiry retry = %#v, %v; want completed expiries before stock recheck", retry, err)
	}
	if owner.state.Batches["batch-2"].RemainingQuantity != 0 || owner.state.Batches["batch-2"].WrittenOffForExpiry != 7000 {
		t.Fatalf("second batch after retry = %#v; want full 7000 expiry", owner.state.Batches["batch-2"])
	}
	if _, found, err := base.FindCommand(ctx, "expiration/batch-2"); err != nil || !found {
		t.Fatalf("second expiry command found=%v err=%v; want retry committed", found, err)
	}
	if _, found, err := base.FindCommand(ctx, request.CommandID); err != nil || found {
		t.Fatalf("order after all stock expired found=%v err=%v; want no OrderPlaced", found, err)
	}
}

func TestAcceptanceUnknownExpiryBlocksOrderUntilResolved(t *testing.T) {
	ctx := context.Background()
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	wrapper := &acceptanceUnknownExpiryStore{EventStore: base, failLookup: true}
	owner, err := NewOwner(wrapper, func() time.Time { return dueAt }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	request := contract.CommitOrderRequest{CommandID: "order-after-unknown-expiry", ProposalID: "proposal-after-unknown-expiry", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}}
	if _, err := owner.CommitAcceptedOrder(ctx, request); !errors.Is(err, contract.ErrInventoryUnavailable) {
		t.Fatalf("CommitAcceptedOrder() while expiry outcome is unknown = %v; want InventoryUnavailable", err)
	}
	if _, found, err := base.FindCommand(ctx, request.CommandID); err != nil || found {
		t.Fatalf("order before resolving expiry found=%v err=%v; want no order", found, err)
	}
	if version, err := base.Version(ctx); err != nil || version != 4 {
		t.Fatalf("journal version during unresolved expiry = %d, %v; want only the committed expiry", version, err)
	}
	wrapper.mu.Lock()
	wrapper.failLookup = false
	wrapper.mu.Unlock()
	result, err := owner.CommitAcceptedOrder(ctx, request)
	if err != nil || result.Kind != contract.CommitResultRecheckRequired || result.Evaluation == nil || len(result.Evaluation.AcceptedPositions) != 0 || result.Evaluation.StateVersion != 4 {
		t.Fatalf("CommitAcceptedOrder() after resolution = %#v, %v; want fresh zero-stock recheck at version 4", result, err)
	}
	if _, found, err := base.FindCommand(ctx, request.CommandID); err != nil || found {
		t.Fatalf("order after expiry resolution found=%v err=%v; want no stale order", found, err)
	}
}

func TestAcceptancePreparationSelectsDuePrefixWithoutFullJournalRead(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, dueAt, 10000, 0)
	appendSecondBatch(t, base, dueAt.Add(time.Hour), 7000)
	reads := 0
	wrapped := &eventStoreWrapper{EventStore: base, read: func(_ int64, batches []domain.RecordedBatch, err error) ([]domain.RecordedBatch, error) {
		reads++
		return batches, err
	}}
	clock := &testClock{now: dueAt.Add(-time.Hour)}
	owner, err := NewOwner(wrapped, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("initial recovery reads = %d; want one initial full replay", reads)
	}
	clock.Set(dueAt)
	state, err := owner.Prepare(context.Background())
	if err != nil || reads != 1 {
		t.Fatalf("Prepare() at due prefix = %#v, %v with %d journal reads; want no full read", state, err, reads)
	}
	if state.State.Batches["batch"].RemainingQuantity != 0 || state.State.Batches["batch-2"].RemainingQuantity != 7000 || len(owner.expirations) != 1 || !owner.expirations["batch-2"].Equal(dueAt.Add(time.Hour)) {
		t.Fatalf("due/future state and queue = %#v/%#v; want only the due prefix processed", state.State.Batches, owner.expirations)
	}
}

func TestAcceptanceCatalogEventsAdvanceVersionWithoutChangingTasks(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, _, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "register-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	request := validDeliveryRequest("receive-p", "delivery-p", now)
	if _, err := owner.ReceiveDelivery(ctx, request); err != nil {
		t.Fatal(err)
	}
	wantTasks := maps.Clone(owner.expirations)
	if len(wantTasks) != 1 {
		t.Fatalf("setup expiration tasks = %#v; want one", wantTasks)
	}
	version := owner.state.StateVersion
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "register-q", ProductID: "q", Name: "Quince", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if owner.state.StateVersion != version+1 || !reflect.DeepEqual(owner.expirations, wantTasks) {
		t.Fatalf("after product registration version/tasks = %d/%#v; want %d and unchanged tasks %#v", owner.state.StateVersion, owner.expirations, version+1, wantTasks)
	}
	version++
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "define-q", DishID: "q-dish", Name: "Quince dish", Ingredients: []contract.Ingredient{{ProductID: "q", Quantity: 500}}}); err != nil {
		t.Fatal(err)
	}
	if owner.state.StateVersion != version+1 || !reflect.DeepEqual(owner.expirations, wantTasks) {
		t.Fatalf("after dish definition version/tasks = %d/%#v; want %d and unchanged tasks %#v", owner.state.StateVersion, owner.expirations, version+1, wantTasks)
	}
}

func TestAcceptanceCommittedDeliveryRepairsFailedTaskAddition(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	base := memory.NewEventStore()
	wrapper := &eventStoreWrapper{EventStore: base, append: func(batch domain.CommandBatch, result domain.AppendResult, err error) (domain.AppendResult, error) {
		if err == nil && batch.CommandType == "ReceiveDelivery" {
			result.Batch.Events[0].Payload.Batches = nil
		}
		return result, err
	}}
	owner, err := NewOwner(wrapper, func() time.Time { return now }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "register-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	receipt, err := owner.ReceiveDelivery(ctx, validDeliveryRequest("receive-p", "delivery-p", now))
	if err != nil || len(receipt.BatchIDs) != 1 {
		t.Fatalf("committed ReceiveDelivery() = %#v, %v; want success despite projection failure", receipt, err)
	}
	if owner.usable {
		t.Fatal("projection remained usable after task addition failed")
	}
	if _, err := owner.Prepare(ctx); err != nil {
		t.Fatalf("Prepare() to rebuild task from committed journal = %v", err)
	}
	batchID := receipt.BatchIDs[0]
	if owner.state.Batches[batchID].RemainingQuantity != 1000 || len(owner.expirations) != 1 || !owner.expirations[batchID].Equal(now.Add(48*time.Hour)) {
		t.Fatalf("recovered batch/index = %#v/%#v; want committed stock and its restored expiry task", owner.state.Batches[batchID], owner.expirations)
	}
}

func TestAcceptanceCommittedSaleRepairsFailedTaskRemoval(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, base, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "register-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "define-d", DishID: "d", Name: "Dish", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 6000}}}); err != nil {
		t.Fatal(err)
	}
	receipt, err := owner.ReceiveDelivery(ctx, deliveryRequest("receive-p", "delivery-p", now, []contract.DeliveryLine{{ProductID: "p", ManufacturedAt: now.Add(-24 * time.Hour), ShelfLife: contract.ShelfLife{Value: 3, Unit: contract.ShelfLifeDay}, ReceivedAt: now, Quantity: 6000}}))
	if err != nil {
		t.Fatal(err)
	}
	batchID := receipt.BatchIDs[0]
	owner.events = &eventStoreWrapper{EventStore: base, append: func(batch domain.CommandBatch, result domain.AppendResult, err error) (domain.AppendResult, error) {
		if err == nil && batch.CommandType == "ConfirmOrder" {
			result.Batch.Events[0].Payload.Order.Allocations[0].Quantity++
		}
		return result, err
	}}
	result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "remove-task-sale", ProposalID: "remove-task-sale", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}})
	if err != nil || result.Order == nil {
		t.Fatalf("committed sale after task removal projection failure = %#v, %v; want recorded order returned", result, err)
	}
	if owner.usable {
		t.Fatal("projection remained usable after task removal failed")
	}
	if _, err := owner.Prepare(ctx); err != nil {
		t.Fatalf("Prepare() to rebuild task removal from journal = %v", err)
	}
	if owner.state.Batches[batchID].RemainingQuantity != 0 || len(owner.expirations) != 0 {
		t.Fatalf("recovered sold batch/index = %#v/%#v; want zero balance and no task", owner.state.Batches[batchID], owner.expirations)
	}
	if _, found, err := base.FindCommand(ctx, "expiration/"+batchID); err != nil || found {
		t.Fatalf("expiry command after recovered full sale found=%v err=%v; want no zero-write-off event", found, err)
	}
}

func TestAcceptanceReadCaptureStaysCoherentAcrossSale(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, now.Add(time.Hour), 10000, 0)
	gate := &acceptanceVersionGateStore{EventStore: base, blockCall: 2, entered: make(chan struct{}), release: make(chan struct{})}
	owner, err := NewOwner(gate, func() time.Time { return now }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	type readResult struct {
		state ReadState
		err   error
	}
	captured := make(chan readResult, 1)
	returnReader := make(chan struct{})
	returned := make(chan readResult, 1)
	go func() {
		state, err := owner.Prepare(ctx)
		captured <- readResult{state: state, err: err}
		<-returnReader
		returned <- readResult{state: state, err: err}
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Prepare did not reach the final version check")
	}
	if owner.mu.TryLock() {
		owner.mu.Unlock()
		t.Fatal("owner serialization gate was released before the read capture completed")
	}
	close(gate.release)
	read := <-captured
	if read.err != nil || !read.state.EvaluatedAt.Equal(now) || read.state.StateVersion != 3 || read.state.State.StateVersion != 3 || read.state.State.Batches["batch"].RemainingQuantity != 10000 {
		t.Fatalf("captured tuple = %#v, %v; want state/time/version from version 3", read.state, read.err)
	}
	first, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "read-interleave-sale", ProposalID: "read-interleave-sale", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}})
	if err != nil || first.Order == nil {
		t.Fatalf("sale between capture and reader return = %#v, %v; want committed order", first, err)
	}
	if read.state.StateVersion != 3 || read.state.State.StateVersion != 3 || read.state.State.Batches["batch"].RemainingQuantity != 10000 {
		t.Fatalf("captured value changed after V+1 sale: %#v", read.state)
	}
	second, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "read-interleave-fresh", ProposalID: "read-interleave-fresh", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}})
	if err != nil || second.Kind != contract.CommitResultRecheckRequired || second.Evaluation == nil || second.Evaluation.StateVersion != 4 || second.Evaluation.Changes[0].Deficits[0].AvailableQuantity != 4000 {
		t.Fatalf("fresh commit after sale = %#v, %v; want recheck using version 4 and 4000 available", second, err)
	}
	close(returnReader)
	if got := <-returned; got.err != nil || got.state.StateVersion != 3 || got.state.State.Batches["batch"].RemainingQuantity != 10000 {
		t.Fatalf("reader result after concurrent sale = %#v, %v; want detached V=3 value", got.state, got.err)
	}
}

func TestAcceptanceReplayRestoresEveryResultKindWithoutNewDecisions(t *testing.T) {
	ctx := context.Background()
	beforeExpiry := moscowTime(2026, time.April, 2, 10)
	afterExpiry := moscowTime(2026, time.April, 4, 10)
	base := memory.NewEventStore()
	clock := &testClock{now: beforeExpiry}
	owner, err := NewOwner(base, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "register-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "define-d", DishID: "d", Name: "Dish", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 500}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ReceiveDelivery(ctx, validDeliveryRequest("receive-d", "delivery-d", beforeExpiry)); err != nil {
		t.Fatal(err)
	}
	if result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "order-d", ProposalID: "proposal-d", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}}); err != nil || result.Order == nil {
		t.Fatalf("seed order = %#v, %v", result, err)
	}
	clock.Set(afterExpiry)
	if _, err := owner.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	batches, err := base.ReadBatches(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[string]domain.CommandResultKind{
		"register-p":               domain.CommandResultProductRegistered,
		"define-d":                 domain.CommandResultDishDefined,
		"receive-d":                domain.CommandResultDeliveryReceived,
		"order-d":                  domain.CommandResultOrderPlaced,
		"expiration/owner-event-3": domain.CommandResultBatchExpired,
	}
	clockCalls, idCalls := 0, 0
	replayed, err := NewOwner(base, func() time.Time { clockCalls++; return afterExpiry }, func() string { idCalls++; return "unexpected-new-id" })
	if err != nil {
		t.Fatal(err)
	}
	if err := replayed.Recover(ctx); err != nil {
		t.Fatalf("Recover() = %v", err)
	}
	if clockCalls != 0 || idCalls != 0 {
		t.Fatalf("replay called clock/ID %d/%d times; want no new time, ID, or decision inputs", clockCalls, idCalls)
	}
	if len(batches) != len(wantKinds) || replayed.state.StateVersion != batches[len(batches)-1].FinalSequence {
		t.Fatalf("journal command count/replayed version = %d/%d; want %d commands through final sequence %d", len(batches), replayed.state.StateVersion, len(wantKinds), batches[len(batches)-1].FinalSequence)
	}
	for _, batch := range batches {
		kind, expected := wantKinds[batch.CommandID]
		record, found, err := replayed.FindRecordedCommand(ctx, batch.CommandID)
		if err != nil {
			t.Fatal(err)
		}
		if !expected || !found || record.CommandType != batch.CommandType || record.CommandFingerprint != batch.CommandFingerprint || record.Result.Kind != kind {
			t.Fatalf("replayed command %q = %#v; want result kind %q and journal fingerprint %q", batch.CommandID, record, kind, batch.CommandFingerprint)
		}
		delete(wantKinds, batch.CommandID)
	}
	if len(wantKinds) != 0 || !reflect.DeepEqual(replayed.state, owner.state) {
		t.Fatalf("replay left expected kinds %v or changed recorded business state", wantKinds)
	}
}

func TestAcceptanceRecoveredDueBatchExpiresBeforeOrderEvaluation(t *testing.T) {
	ctx := context.Background()
	dueAt := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, dueAt, 10000, 0)
	snapshots := memory.NewSnapshotStore()
	checkpointOwner, err := NewOwner(store, func() time.Time { return dueAt.Add(-time.Hour) }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpointOwner.ConfigureSnapshots(snapshots, 1); err != nil {
		t.Fatal(err)
	}
	if err := checkpointOwner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if worked, err := checkpointOwner.RunSnapshotOnce(ctx); !worked || err != nil {
		t.Fatalf("save checkpoint = %v, %v; want journal snapshot before expiry", worked, err)
	}
	ids := 0
	recovered, err := NewOwner(store, func() time.Time { return dueAt }, func() string { ids++; return fmt.Sprintf("recovered-event-%d", ids) })
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.ConfigureSnapshots(snapshots, 1); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if recovered.lastSavedSequence != 3 {
		t.Fatalf("accepted recovery checkpoint = %d; want snapshot sequence 3", recovered.lastSavedSequence)
	}
	evaluation, err := recovered.EvaluateOrder(ctx, contract.EvaluateOrderRequest{Positions: []contract.Position{{LineNo: 1, DishID: "d", Portions: 1}}})
	if err != nil || len(evaluation.AcceptedPositions) != 0 || len(evaluation.Changes) != 1 || evaluation.Changes[0].Deficits[0].RequiredQuantity != 6000 || evaluation.Changes[0].Deficits[0].AvailableQuantity != 0 || evaluation.StateVersion != 4 {
		t.Fatalf("order evaluation after snapshot recovery = %#v, %v; want due batch removed before evaluation", evaluation, err)
	}
	command, found, err := store.FindCommand(ctx, "expiration/batch")
	if err != nil || !found || command.Result.Expiry == nil || command.Result.Expiry.WrittenOffQuantity != 10000 {
		t.Fatalf("recovered expiry = %#v, found %v, err %v; want full write-off before dish evaluation", command, found, err)
	}
}

func TestAcceptanceCommittedCommandSurvivesSnapshotSaveFailureAndRecovery(t *testing.T) {
	ctx := context.Background()
	store := memory.NewEventStore()
	snapshots := &acceptanceFailOnceSnapshotStore{SnapshotStore: memory.NewSnapshotStore()}
	now := moscowTime(2026, time.April, 2, 10)
	owner, err := NewOwner(store, func() time.Time { return now }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(snapshots, 1); err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	request := contract.RegisterProductRequest{CommandID: "saved-business-command", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}
	original, err := owner.RegisterProduct(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := owner.RunSnapshotOnce(ctx); !worked || err == nil {
		t.Fatalf("snapshot Save after business operation = %v, %v; want save failure after committed event", worked, err)
	}
	clockCalls, idCalls := 0, 0
	recovered, err := NewOwner(store, func() time.Time { clockCalls++; return now }, func() string { idCalls++; return "unexpected-retry-id" })
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := recovered.RegisterProduct(ctx, request)
	if err != nil || replayed != original {
		t.Fatalf("replayed business command = %#v, %v; want original %#v", replayed, err, original)
	}
	if clockCalls != 0 || idCalls != 0 {
		t.Fatalf("recovery/retry called clock/ID %d/%d times; want return from journal without a new decision", clockCalls, idCalls)
	}
	if version, err := store.Version(ctx); err != nil || version != 1 {
		t.Fatalf("version after snapshot retry/recovery = %d, %v; want one committed business event", version, err)
	}
}

func TestAcceptanceSnapshotSavedSequenceAdvancesOnlyAfterSuccessfulSave(t *testing.T) {
	ctx := context.Background()
	store := seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)
	baseSnapshots := memory.NewSnapshotStore()
	snapshots := &acceptanceFailOnceSnapshotStore{SnapshotStore: baseSnapshots}
	owner, err := NewOwner(store, func() time.Time { return moscowTime(2026, time.April, 1, 10) }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(snapshots, 1); err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if owner.lastSavedSequence != 0 {
		t.Fatalf("lastSavedSequence after offering candidate = %d; want no acknowledged save", owner.lastSavedSequence)
	}
	if worked, err := owner.RunSnapshotOnce(ctx); !worked || err == nil {
		t.Fatalf("first snapshot Save = %v, %v; want offered candidate with failed Save", worked, err)
	}
	if owner.lastSavedSequence != 0 {
		t.Fatalf("lastSavedSequence after failed Save = %d; want previous acknowledged sequence 0", owner.lastSavedSequence)
	}
	if worked, err := owner.RunSnapshotOnce(ctx); !worked || err != nil {
		t.Fatalf("snapshot retry = %v, %v; want successful retry of same candidate", worked, err)
	}
	saved, found, err := baseSnapshots.LoadLatest(ctx)
	if err != nil || !found || saved.LastSequence != 3 || owner.lastSavedSequence != 3 {
		t.Fatalf("saved sequence/owner acknowledgment = %d/%d, found %v, err %v; want confirmed candidate 3", saved.LastSequence, owner.lastSavedSequence, found, err)
	}
}

func TestAcceptanceSnapshotRetryDoesNotRepeatBusinessCommand(t *testing.T) {
	ctx := context.Background()
	base := memory.NewEventStore()
	snapshotsBase := memory.NewSnapshotStore()
	snapshots := &acceptanceFailOnceSnapshotStore{SnapshotStore: snapshotsBase}
	now := moscowTime(2026, time.April, 2, 10)
	owner, err := NewOwner(base, func() time.Time { return now }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(snapshots, 1); err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "retry-product", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	beforeBatches, err := base.ReadBatches(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	beforeState := domain.CloneInventoryState(owner.state)
	if worked, err := owner.RunSnapshotOnce(ctx); !worked || err == nil {
		t.Fatalf("first snapshot Save = %v, %v; want failed save", worked, err)
	}
	if worked, err := owner.RunSnapshotOnce(ctx); !worked || err != nil {
		t.Fatalf("snapshot retry = %v, %v; want success without new business input", worked, err)
	}
	afterBatches, err := base.ReadBatches(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := base.Version(ctx); err != nil || version != 1 || !reflect.DeepEqual(beforeBatches, afterBatches) || !reflect.DeepEqual(beforeState, owner.state) {
		t.Fatalf("business data changed during snapshot retry: version=%d, err=%v, batches equal=%v, state equal=%v", version, err, reflect.DeepEqual(beforeBatches, afterBatches), reflect.DeepEqual(beforeState, owner.state))
	}
	command, found, err := base.FindCommand(ctx, "retry-product")
	if err != nil || !found || command.Result.Kind != domain.CommandResultProductRegistered || command.Result.Product == nil || command.Result.Product.ProductID != "p" {
		t.Fatalf("business command after snapshot retry = %#v, found %v, err %v; want original product result", command, found, err)
	}
}

type acceptanceUnknownExpiryStore struct {
	EventStore
	mu         sync.Mutex
	failLookup bool
	unknown    bool
}

type acceptanceConcurrentAppendBarrierStore struct {
	EventStore
	mu        sync.Mutex
	arrivals  chan domain.CommandBatch
	release   chan struct{}
	seen      map[string]bool
	conflicts int
}

func (s *acceptanceConcurrentAppendBarrierStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	block := false
	if batch.CommandType == "ReceiveDelivery" {
		s.mu.Lock()
		if !s.seen[batch.CommandID] {
			s.seen[batch.CommandID] = true
			block = true
		}
		s.mu.Unlock()
	}
	if block {
		s.arrivals <- *cloneCommandBatch(batch)
		select {
		case <-s.release:
		case <-ctx.Done():
			return domain.AppendResult{}, ctx.Err()
		}
	}
	result, err := s.EventStore.AppendBatch(ctx, batch)
	if errors.Is(err, domain.ErrConcurrentModification) {
		s.mu.Lock()
		s.conflicts++
		s.mu.Unlock()
	}
	return result, err
}

func prefixedAcceptanceID(prefix string) func() string {
	var next int
	return func() string {
		next++
		return fmt.Sprintf("%s-%d", prefix, next)
	}
}

func (s *acceptanceUnknownExpiryStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	result, err := s.EventStore.AppendBatch(ctx, batch)
	if batch.CommandID == "expiration/batch" && err == nil && !s.unknown {
		s.unknown = true
		return domain.AppendResult{}, domain.ErrUnknownWriteOutcome
	}
	return result, err
}

func (s *acceptanceUnknownExpiryStore) FindCommand(ctx context.Context, commandID string) (domain.CommandRecord, bool, error) {
	s.mu.Lock()
	fail := s.failLookup && commandID == "expiration/batch"
	s.mu.Unlock()
	if fail {
		return domain.CommandRecord{}, false, errors.New("expiry result temporarily unavailable")
	}
	return s.EventStore.FindCommand(ctx, commandID)
}

type acceptanceVersionGateStore struct {
	EventStore
	mu        sync.Mutex
	blockCall int
	calls     int
	entered   chan struct{}
	release   chan struct{}
}

func (s *acceptanceVersionGateStore) Version(ctx context.Context) (int64, error) {
	s.mu.Lock()
	s.calls++
	block := s.calls == s.blockCall
	s.mu.Unlock()
	if block {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return s.EventStore.Version(ctx)
}

type acceptanceFailOnceSnapshotStore struct {
	SnapshotStore
	mu     sync.Mutex
	failed bool
}

func (s *acceptanceFailOnceSnapshotStore) Save(ctx context.Context, snapshot domain.Snapshot) error {
	s.mu.Lock()
	if !s.failed {
		s.failed = true
		s.mu.Unlock()
		return errors.New("injected snapshot save failure")
	}
	s.mu.Unlock()
	return s.SnapshotStore.Save(ctx, snapshot)
}
