package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func w3Scope() contract.Scope {
	return contract.Scope{OrganizationID: "22222222-2222-4222-8222-222222222222", RestaurantID: "33333333-3333-4333-8333-333333333333", RuntimeEpoch: "44444444-4444-4444-8444-444444444444", LifecycleVersion: "1", TimeZone: "Europe/Moscow", SettingsVersion: "1"}
}
func w3Owner(t *testing.T, store EventStore, clock *testClock) *Owner {
	t.Helper()
	id := 0
	owner, err := NewOwner(store, clock.Now, func() string { id++; return string(rune('a' + id)) })
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureScope(w3Scope()); err != nil {
		t.Fatal(err)
	}
	return owner
}

const w3Actor = "11111111-1111-4111-8111-111111111111"

func TestW3BoundaryBeforeSchedulerAndConcurrentWriteoff(t *testing.T) {
	expiry := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, expiry, 10000, 6000)
	clock := &testClock{now: expiry.Add(-time.Nanosecond)}
	owner := w3Owner(t, store, clock)
	ctx := context.Background()
	state, err := owner.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := state.StateVersion
	clock.Set(expiry)
	state, err = owner.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := state.State.Batches["batch"]
	if !b.PendingWriteoff || b.RemainingQuantity != 4000 || b.WrittenOffForExpiry != 0 || state.StateVersion != before+1 {
		t.Fatalf("pending: %+v", b)
	}
	snapshot := domain.Snapshot{SchemaVersion: domain.SnapshotSchemaV2, StreamID: domain.InventoryStreamID, LastSequence: state.StateVersion, State: state.State}
	if err := domain.ValidateSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	clone := domain.CloneSnapshot(snapshot)
	corrupt := clone.State.Batches["batch"]
	corrupt.RemainingQuantity = 1
	clone.State.Batches["batch"] = corrupt
	if snapshot.State.Batches["batch"].RemainingQuantity != 4000 {
		t.Fatal("pending alias")
	}
	if err := domain.ValidateSnapshot(clone); err == nil {
		t.Fatal("corrupt pending snapshot accepted")
	}
	request := contract.ConfirmWriteoffRequest{CommandID: "once", BatchID: "batch", ActorUserID: w3Actor}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			v, err := owner.ConfirmWriteoff(ctx, request)
			if err != nil || v.WrittenOffQuantity != 4000 {
				t.Errorf("confirmation=%+v %v", v, err)
			}
		})
	}
	wg.Wait()
	original, err := owner.ConfirmWriteoff(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(expiry.Add(time.Hour))
	request.ActorUserID = "11111111-1111-4111-8111-111111111112"
	replay, err := owner.ConfirmWriteoff(ctx, request)
	if err != nil || original != replay {
		t.Fatalf("mutable replay %+v %+v %v", original, replay, err)
	}
	request.BatchID = "different"
	if _, err := owner.ConfirmWriteoff(ctx, request); !errors.Is(err, contract.ErrCommandIdentityConflict) {
		t.Fatalf("retarget=%v", err)
	}
	request.CommandID = "other"
	request.BatchID = "batch"
	if _, err := owner.ConfirmWriteoff(ctx, request); !errors.Is(err, contract.ErrAlreadyWrittenOff) {
		t.Fatalf("second command=%v", err)
	}
	history, err := owner.GetHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pending, final := 0, 0
	for _, e := range history.Events {
		if e.Type == "BatchExpiryPending" {
			pending++
		}
		if e.Type == "BatchWriteoffConfirmed" {
			final++
			if e.SchemaVersion != 2 || e.Payload.Writeoff.ActorUserID != w3Actor || e.TenantID != w3Scope().RestaurantID {
				t.Fatalf("event=%+v", e)
			}
		}
	}
	if pending != 1 || final != 1 {
		t.Fatalf("facts=%d/%d", pending, final)
	}
	restored := w3Owner(t, store, clock)
	if err := restored.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = restored.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b = state.State.Batches["batch"]
	if b.RemainingQuantity != 0 || b.WrittenOffForExpiry != 4000 || b.ConsumedByOrders != 6000 {
		t.Fatalf("replay conservation=%+v", b)
	}
	snapshot = domain.Snapshot{SchemaVersion: domain.SnapshotSchemaV2, StreamID: domain.InventoryStreamID, LastSequence: state.StateVersion, State: state.State}
	if err := domain.ValidateSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestW3UnknownConfirmationResolvesOriginalFact(t *testing.T) {
	expiry := moscowTime(2026, time.April, 2, 10)
	base := seedStore(t, expiry, 10000, 0)
	clock := &testClock{now: expiry}
	store := &eventStoreWrapper{EventStore: base, append: func(c domain.CommandBatch, r domain.AppendResult, e error) (domain.AppendResult, error) {
		if c.CommandType == "ConfirmWriteoff" && e == nil {
			return domain.AppendResult{}, domain.ErrUnknownWriteOutcome
		}
		return r, e
	}}
	owner := w3Owner(t, store, clock)
	v, err := owner.ConfirmWriteoff(context.Background(), contract.ConfirmWriteoffRequest{CommandID: "unknown", BatchID: "batch", ActorUserID: w3Actor})
	if err != nil || v.WrittenOffQuantity != 10000 {
		t.Fatalf("real commit readback: %+v %v", v, err)
	}
	before, err := base.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	observation, err := owner.ResolveWriteoff(context.Background(), "unknown", "batch")
	if err != nil || observation.State != "committed" || *observation.Result != v {
		t.Fatalf("observation %+v %v", observation, err)
	}
	after, err := base.Version(context.Background())
	if err != nil || after != before {
		t.Fatal("passive lookup mutated store")
	}
}

func TestW3AvailableBatchCannotBeConfirmed(t *testing.T) {
	expiry := moscowTime(2026, time.April, 2, 10)
	owner := w3Owner(t, seedStore(t, expiry, 10000, 0), &testClock{now: expiry.Add(-time.Nanosecond)})
	if _, err := owner.ConfirmWriteoff(context.Background(), contract.ConfirmWriteoffRequest{CommandID: "early", BatchID: "batch", ActorUserID: w3Actor}); !errors.Is(err, contract.ErrNotPendingWriteoff) {
		t.Fatal(err)
	}
}

func TestW3MixedLegacyHistoryAndSnapshotSuffix(t *testing.T) {
	ctx := context.Background()
	expiry := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, expiry, 10000, 0)
	legacy, err := NewOwner(store, func() time.Time { return expiry }, func() string { return "legacy-expiry" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExpireBatch(ctx, contract.ExpireBatchRequest{CommandID: "expiration/batch", BatchID: "batch"}); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: expiry}
	snapshots := memory.NewSnapshotStore()
	owner := w3Owner(t, store, clock)
	if err := owner.ConfigureSnapshots(snapshots, 1); err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	receipt, err := owner.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "second", DeliveryID: "second", Number: "2", CreatedAt: expiry, Lines: []contract.DeliveryLine{{ProductID: "p", Quantity: 100, ManufacturedAt: expiry.Add(-24 * time.Hour), ReceivedAt: expiry, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := owner.RunSnapshotOnce(ctx); err != nil || !worked {
		t.Fatalf("pending snapshot=%v %v", worked, err)
	}
	saved, found, err := snapshots.LoadLatest(ctx)
	if err != nil || !found {
		t.Fatal(err)
	}
	if _, err := owner.ConfirmWriteoff(ctx, contract.ConfirmWriteoffRequest{CommandID: "confirm-second", BatchID: receipt.BatchIDs[0], ActorUserID: w3Actor}); err != nil {
		t.Fatal(err)
	}
	restored := w3Owner(t, store, clock)
	if err := restored.ConfigureSnapshots(snapshots, 1); err != nil {
		t.Fatal(err)
	}
	if err := restored.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if restored.lastSavedSequence != saved.LastSequence {
		t.Fatal("snapshot checkpoint was not used")
	}
	prepared, err := restored.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State.Batches["batch"].WrittenOffForExpiry != 10000 || prepared.State.Batches[receipt.BatchIDs[0]].WrittenOffForExpiry != 100 {
		t.Fatal("old/new conservation changed")
	}
	history, err := restored.GetHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old, pending, confirmed := 0, 0, 0
	for _, e := range history.Events {
		switch e.Type {
		case "BatchExpired":
			old++
			if e.SchemaVersion != 1 {
				t.Fatal("legacy schema")
			}
		case "BatchExpiryPending":
			pending++
		case "BatchWriteoffConfirmed":
			confirmed++
		}
	}
	if old != 1 || pending != 1 || confirmed != 1 {
		t.Fatalf("mixed facts %d/%d/%d", old, pending, confirmed)
	}
}

func TestW3CompetingCommandsWriteOneResidualMovement(t *testing.T) {
	ctx := context.Background()
	expiry := moscowTime(2026, time.April, 2, 10)
	owner := w3Owner(t, seedStore(t, expiry, 10000, 6000), &testClock{now: expiry})
	var mu sync.Mutex
	committed, refused := 0, 0
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_, err := owner.ConfirmWriteoff(ctx, contract.ConfirmWriteoffRequest{CommandID: fmt.Sprintf("command-%d", i), BatchID: "batch", ActorUserID: w3Actor})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				committed++
			} else if errors.Is(err, contract.ErrAlreadyWrittenOff) {
				refused++
			} else {
				t.Errorf("unexpected outcome %v", err)
			}
		})
	}
	wg.Wait()
	if committed != 1 || refused != 19 {
		t.Fatalf("outcomes=%d/%d", committed, refused)
	}
	history, err := owner.GetHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range history.Events {
		if e.Type == "BatchWriteoffConfirmed" {
			count++
			if e.Payload.Writeoff.WrittenOffQuantity != 4000 {
				t.Fatal("wrong residual")
			}
		}
	}
	if count != 1 {
		t.Fatalf("final movements=%d", count)
	}
}

func TestW3ConfirmationCapturesRequestSettingsAndRetainsRetry(t *testing.T) {
	expiry := moscowTime(2026, time.April, 2, 10)
	owner := w3Owner(t, seedStore(t, expiry, 10000, 0), &testClock{now: expiry})
	captured := w3Scope()
	captured.TimeZone = "Europe/Berlin"
	captured.SettingsVersion = "2"
	request := contract.ConfirmWriteoffRequest{Scope: captured, CommandID: "settings-once", BatchID: "batch", ActorUserID: w3Actor}
	original, err := owner.ConfirmWriteoff(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if original.TimeZone != "Europe/Berlin" || original.SettingsVersion != "2" {
		t.Fatalf("fresh confirmation lost captured settings: %+v", original)
	}
	request.Scope.TimeZone = "Europe/Paris"
	request.Scope.SettingsVersion = "3"
	replay, err := owner.ConfirmWriteoff(context.Background(), request)
	if err != nil || original != replay {
		t.Fatalf("retained retry recomputed settings: %+v %v", replay, err)
	}
}
