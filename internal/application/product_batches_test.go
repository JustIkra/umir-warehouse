package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

func TestProductBatchesCaptureFEFOAndImmutablePages(t *testing.T) {
	at := moscowTime(2026, time.April, 2, 10)
	business := &testClock{now: at}
	physical := &testClock{now: at}
	owner := w3Owner(t, seedStore(t, at, 10000, 6000), business)
	for i := range 2 {
		_, err := owner.ReceiveDelivery(context.Background(), contract.ReceiveDeliveryRequest{
			CommandID: fmt.Sprintf("ua1-delivery-%d", i), DeliveryID: fmt.Sprintf("ua1-delivery-%d", i),
			Number: "invoice", CreatedAt: at.Add(-23 * time.Hour),
			Lines: []contract.DeliveryLine{{ProductID: "p", Quantity: 7000,
				ManufacturedAt: at.Add(-23 * time.Hour), ReceivedAt: at.Add(-22 * time.Hour),
				ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	reader, err := NewProductBatches(owner, physical.Now, func() string { n++; return fmt.Sprintf("batch-cursor-%d", n) }, batchTestAfter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	r := contract.ProductBatchesRequest{Scope: w3Scope(), Actor: w3Actor, ProductID: "p", Limit: 1}
	first, err := reader.Read(context.Background(), r)
	if err != nil || len(first.Items) != 1 || first.Items[0].Batch.BatchID != "batch" || !first.Items[0].Batch.PendingWriteoff || first.Items[0].Batch.RemainingQuantity != 4000 || first.NextCursor == "" {
		t.Fatalf("first FEFO page %+v, %v", first, err)
	}
	if first.Identity.StateVersion != first.Identity.PrefixSequence {
		t.Fatal("capture prefix is not the committed state")
	}
	if _, err := owner.ConfirmWriteoff(context.Background(), contract.ConfirmWriteoffRequest{Scope: w3Scope(), ActorUserID: w3Actor, CommandID: "ua1-writeoff", BatchID: "batch"}); err != nil {
		t.Fatal(err)
	}
	first.Items[0].Batch.RemainingQuantity = 1
	r.Cursor = first.NextCursor
	second, err := reader.Read(context.Background(), r)
	if err != nil || second.Identity != first.Identity || len(second.Items) != 1 || second.Items[0].Batch.PendingWriteoff || second.Items[0].Batch.RemainingQuantity != 7000 {
		t.Fatalf("captured second page %+v %v", second, err)
	}
	second.Items[0].Batch.RemainingQuantity = 1
	again, err := reader.Read(context.Background(), r)
	if err != nil || again.Items[0].Batch.RemainingQuantity != 7000 {
		t.Fatal("returned items alias captured rows")
	}
	r.Cursor = second.NextCursor
	third, err := reader.Read(context.Background(), r)
	if err != nil || third.NextCursor != "" || third.Items[0].Batch.BatchID <= again.Items[0].Batch.BatchID {
		t.Fatalf("FEFO tie/end %+v %v", third, err)
	}
	r.Cursor = ""
	fresh, err := reader.Read(context.Background(), r)
	if err != nil || fresh.Items[0].Batch.BatchID == "batch" || fresh.Identity.StateVersion <= first.Identity.StateVersion {
		t.Fatal("fresh capture did not observe confirmed writeoff")
	}
}

func TestProductBatchesCursorBindingsAndPhysicalExpiry(t *testing.T) {
	at := moscowTime(2026, time.April, 1, 10)
	business, physical := &testClock{now: at}, &testClock{now: at}
	owner := w3Owner(t, seedStore(t, at.Add(24*time.Hour), 10000, 0), business)
	_, err := owner.ReceiveDelivery(context.Background(), contract.ReceiveDeliveryRequest{CommandID: "ua1-extra", DeliveryID: "extra", Number: "extra", CreatedAt: at, Lines: []contract.DeliveryLine{{ProductID: "p", Quantity: 1000, ManufacturedAt: at, ReceivedAt: at, ShelfLife: contract.ShelfLife{Value: 2, Unit: contract.ShelfLifeDay}}}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewProductBatches(owner, physical.Now, func() string { return "opaque-handle" }, batchTestAfter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	r := contract.ProductBatchesRequest{Scope: w3Scope(), Actor: w3Actor, ProductID: "p", Limit: 1}
	first, err := reader.Read(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Cursor = first.NextCursor
	for _, mutate := range []func(*contract.ProductBatchesRequest){
		func(v *contract.ProductBatchesRequest) { v.Actor = "other" },
		func(v *contract.ProductBatchesRequest) { v.ProductID = "other" },
		func(v *contract.ProductBatchesRequest) { v.Limit = 2 },
		func(v *contract.ProductBatchesRequest) { v.Scope.SettingsVersion = "2" },
	} {
		changed := r
		mutate(&changed)
		if _, err := reader.Read(context.Background(), changed); !errors.Is(err, contract.ErrRefreshRequired) {
			t.Fatalf("cursor accepted another binding: %v", err)
		}
	}
	business.Set(at.Add(48 * time.Hour))
	if _, err := reader.Read(context.Background(), r); err != nil {
		t.Fatalf("business clock expired physical cursor: %v", err)
	}
	physical.Set(at.Add(5 * time.Minute))
	if _, err := reader.Read(context.Background(), r); !errors.Is(err, contract.ErrRefreshRequired) {
		t.Fatalf("expired cursor accepted: %v", err)
	}
	r.Cursor, r.ProductID = "", "missing"
	if _, err := reader.Read(context.Background(), r); !errors.Is(err, contract.ErrProductNotFound) {
		t.Fatalf("unknown product: %v", err)
	}
}

func batchTestAfter(d time.Duration, f func()) PageExpiryTimer {
	return time.AfterFunc(d, f)
}

func TestProductBatchesSnapshotCapacityAndCleanup(t *testing.T) {
	at := moscowTime(2026, time.April, 1, 10)
	clock := &testClock{now: at}
	owner := w3Owner(t, seedStore(t, at.Add(24*time.Hour), 10000, 0), clock)
	_, err := owner.ReceiveDelivery(context.Background(), contract.ReceiveDeliveryRequest{CommandID: "capacity-extra", DeliveryID: "capacity-extra", Number: "extra", CreatedAt: at, Lines: []contract.DeliveryLine{{ProductID: "p", Quantity: 1000, ManufacturedAt: at, ReceivedAt: at, ShelfLife: contract.ShelfLife{Value: 2, Unit: contract.ShelfLifeDay}}}})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	reader, err := NewProductBatches(owner, clock.Now, func() string { n++; return fmt.Sprintf("capacity-%d", n) }, batchTestAfter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	r := contract.ProductBatchesRequest{Scope: w3Scope(), Actor: w3Actor, ProductID: "p", Limit: 1}
	for range 128 {
		if _, err := reader.Read(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reader.Read(context.Background(), r); !errors.Is(err, contract.ErrLimitExceeded) {
		t.Fatalf("capture capacity bypassed: %v", err)
	}
	clock.Set(at.Add(5 * time.Minute))
	page, err := reader.Read(context.Background(), r)
	if err != nil {
		t.Fatalf("expired captures retained capacity: %v", err)
	}
	reader.Close()
	r.Cursor = page.NextCursor
	if _, err := reader.Read(context.Background(), r); !errors.Is(err, contract.ErrRefreshRequired) {
		t.Fatalf("closed capture remained usable: %v", err)
	}
}
