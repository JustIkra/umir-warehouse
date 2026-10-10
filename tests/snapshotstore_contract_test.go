package tests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestSnapshotStoreCopiesAndPublishesMonotonically(t *testing.T) {
	store := memory.NewSnapshotStore()
	ctx := context.Background()
	candidate := validSnapshot(2)
	candidate.State.Products["product-1"] = domain.Product{ProductID: "product-1", Name: "Tomato", BaseUnit: domain.BaseUnitG}
	candidate.State.Dishes = map[string]domain.Dish{"dish-1": {DishID: "dish-1", Name: "Soup", Ingredients: []domain.Ingredient{{ProductID: "product-1", Quantity: 500}}}}
	candidate.State.Commands["command-1"] = productCommand("command-1", "product-1")
	candidate.State.Commands["command-2"] = dishCommand("command-2", "dish-1", "product-1")

	if err := store.Save(ctx, candidate); err != nil {
		t.Fatalf("Save(valid candidate) error = %v", err)
	}
	candidate.State.Dishes["dish-1"].Ingredients[0].Quantity = 900
	loaded, found, err := store.LoadLatest(ctx)
	if err != nil || !found {
		t.Fatalf("LoadLatest() = found %v, err %v", found, err)
	}
	if loaded.State.Dishes["dish-1"].Ingredients[0].Quantity != 500 {
		t.Fatal("store retained caller mutation after Save")
	}
	loaded.State.Dishes["dish-1"].Ingredients[0].Quantity = 100
	reloaded, _, err := store.LoadLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Dishes["dish-1"].Ingredients[0].Quantity != 500 {
		t.Fatal("LoadLatest exposed mutable store state")
	}

	older := productSnapshot()
	if err := store.Save(ctx, older); err != nil {
		t.Fatalf("Save(older valid candidate) error = %v", err)
	}
	latest, found, err := store.LoadLatest(ctx)
	if err != nil || !found || latest.LastSequence != 2 {
		t.Fatalf("latest after older Save() = %#v, found %v, err %v; want sequence 2", latest, found, err)
	}
}

func TestSnapshotStoreRejectsInvalidCandidateWithoutReplacingPrevious(t *testing.T) {
	store := memory.NewSnapshotStore()
	ctx := context.Background()
	if err := store.Save(ctx, validSnapshot(0)); err != nil {
		t.Fatal(err)
	}
	invalid := validSnapshot(2)
	invalid.SchemaVersion = 99
	if err := store.Save(ctx, invalid); !errors.Is(err, domain.ErrUnsupportedSchema) {
		t.Fatalf("Save(unsupported schema) error = %v; want ErrUnsupportedSchema", err)
	}
	invalid = validSnapshot(2)
	invalid.State.StateVersion = 1
	if err := store.Save(ctx, invalid); err == nil {
		t.Fatal("Save accepted state version different from snapshot sequence")
	}
	invalid = validSnapshot(2)
	invalid.State.Products = map[string]domain.Product{"product-1": {ProductID: "product-1", Name: "Tomato", BaseUnit: domain.BaseUnitG}}
	invalid.State.Dishes = map[string]domain.Dish{"dish-1": {DishID: "dish-1", Name: "Soup", Ingredients: []domain.Ingredient{{ProductID: "missing", Quantity: 500}}}}
	invalid.State.Commands["command-1"] = productCommand("command-1", "product-1")
	invalid.State.Commands["command-2"] = dishCommand("command-2", "dish-1", "missing")
	if err := store.Save(ctx, invalid); err == nil {
		t.Fatal("Save accepted a dish referencing an unknown product")
	}
	latest, found, err := store.LoadLatest(ctx)
	if err != nil || !found || latest.LastSequence != 0 {
		t.Fatalf("previous snapshot after rejected candidates = %#v, found %v, err %v; want sequence 0", latest, found, err)
	}
}

func TestSnapshotStoreCancellationBeforeCommitPreservesPrevious(t *testing.T) {
	store := memory.NewSnapshotStore()
	if err := store.Save(context.Background(), validSnapshot(0)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Save(ctx, validSnapshot(2)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save(canceled context) error = %v; want context.Canceled", err)
	}
	latest, found, err := store.LoadLatest(context.Background())
	if err != nil || !found || latest.LastSequence != 0 {
		t.Fatalf("previous snapshot after canceled Save() = %#v, found %v, err %v; want sequence 0", latest, found, err)
	}
}

func TestSnapshotStoreAcceptsImmediateDeliveryExpiration(t *testing.T) {
	store := memory.NewSnapshotStore()
	state := domain.InventoryState{}
	registeredAt := snapshotTime(2026, time.March, 1, 12)
	productBatch := domain.RecordedBatch{
		CommandID: "register-product", CommandType: "RegisterProduct", CommandFingerprint: "product-fingerprint", FinalSequence: 1,
		Events: []domain.Event{{
			EventID: "product-event", StreamID: domain.InventoryStreamID, Sequence: 1, SchemaVersion: domain.InventorySchemaV1,
			CommandID: "register-product", CommandFingerprint: "product-fingerprint", RecordedAt: registeredAt, Type: domain.EventProductRegistered,
			Payload: domain.EventPayload{Product: &domain.Product{ProductID: "product-1", Name: "Tomato", BaseUnit: domain.BaseUnitG}},
		}},
	}
	var err error
	state, err = domain.ApplyRecordedBatch(state, productBatch)
	if err != nil {
		t.Fatal(err)
	}
	manufacturedAt := snapshotTime(2026, time.February, 28, 12)
	arrivalAt := snapshotTime(2026, time.March, 1, 12)
	delivery := domain.Delivery{DeliveryID: "delivery-1", Number: "invoice-1", CreatedAt: arrivalAt, Lines: []domain.DeliveryLine{{ProductID: "product-1", ManufacturedAt: manufacturedAt, ShelfLife: domain.ShelfLife{Value: 1, Unit: domain.ShelfLifeDay}, ReceivedAt: arrivalAt, Quantity: 1000}}}
	batch := domain.Batch{BatchID: "batch-1", DeliveryID: "delivery-1", LineNo: 1, ProductID: "product-1", ManufacturedAt: manufacturedAt, ShelfLife: domain.ShelfLife{Value: 1, Unit: domain.ShelfLifeDay}, ReceivedAt: arrivalAt, ExpiresAt: arrivalAt, ReceivedQuantity: 1000, RemainingQuantity: 1000}
	deliveryBatch := domain.RecordedBatch{
		CommandID: "receive-delivery", CommandType: "ReceiveDelivery", CommandFingerprint: "delivery-fingerprint", FinalSequence: 3,
		Events: []domain.Event{
			{EventID: "delivery-event", StreamID: domain.InventoryStreamID, Sequence: 2, SchemaVersion: domain.InventorySchemaV1, CommandID: "receive-delivery", CommandFingerprint: "delivery-fingerprint", RecordedAt: arrivalAt, Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{Delivery: &delivery, Batches: []domain.Batch{batch}}},
			{EventID: "expiry-event", StreamID: domain.InventoryStreamID, Sequence: 3, SchemaVersion: domain.InventorySchemaV1, CommandID: "receive-delivery", CommandFingerprint: "delivery-fingerprint", RecordedAt: arrivalAt, Type: domain.EventBatchExpired, Payload: domain.EventPayload{Expiry: &domain.ExpiryResult{BatchID: "batch-1", ProductID: "product-1", WrittenOffQuantity: 1000, Reason: domain.ExpiryReasonExpiration, ExpiresAt: arrivalAt, ProcessedAt: arrivalAt}}},
		},
	}
	state, err = domain.ApplyRecordedBatch(state, deliveryBatch)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := validSnapshot(state.StateVersion)
	snapshot.State = state
	if err := store.Save(context.Background(), snapshot); err != nil {
		t.Fatalf("Save(reducer-produced immediate-expiry state) error = %v", err)
	}
}

func TestSnapshotStoreRejectsDuplicateCommandCoverageAndPreservesPrevious(t *testing.T) {
	candidate := validSnapshot(2)
	candidate.State.Products = map[string]domain.Product{
		"product-1": {ProductID: "product-1", Name: "Tomato", BaseUnit: domain.BaseUnitG},
		"product-2": {ProductID: "product-2", Name: "Potato", BaseUnit: domain.BaseUnitG},
	}
	candidate.State.Commands["command-1"] = productCommand("command-1", "product-1")
	candidate.State.Commands["command-2"] = productCommand("command-2", "product-1")
	assertSnapshotRejectedWithoutReplacement(t, candidate)
}

func TestSnapshotStoreRejectsRemainingStockAfterWriteOffAndPreservesPrevious(t *testing.T) {
	candidate := snapshotWithExpiry(500, 500, snapshotTime(2026, time.March, 3, 12))
	assertSnapshotRejectedWithoutReplacement(t, candidate)
}

func TestSnapshotStoreRejectsInvalidExpiryProcessedAtAndPreservesPrevious(t *testing.T) {
	for _, test := range []struct {
		name        string
		processedAt time.Time
	}{
		{name: "zero time", processedAt: time.Time{}},
		{name: "unsupported offset", processedAt: time.Date(2026, time.March, 3, 12, 0, 0, 0, time.FixedZone("UTC", 0))},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := snapshotWithExpiry(0, 1000, test.processedAt)
			assertSnapshotRejectedWithoutReplacement(t, candidate)
		})
	}
}

func assertSnapshotRejectedWithoutReplacement(t *testing.T, candidate domain.Snapshot) {
	t.Helper()
	store := memory.NewSnapshotStore()
	if err := store.Save(context.Background(), validSnapshot(0)); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), candidate); err == nil {
		t.Fatal("Save accepted an invalid snapshot")
	}
	previous, found, err := store.LoadLatest(context.Background())
	if err != nil || !found || previous.LastSequence != 0 {
		t.Fatalf("previous snapshot = %#v, found %v, err %v; want sequence 0", previous, found, err)
	}
}

func snapshotWithExpiry(remaining, writtenOff domain.Quantity, processedAt time.Time) domain.Snapshot {
	manufacturedAt := snapshotTime(2026, time.February, 28, 12)
	receivedAt := snapshotTime(2026, time.March, 1, 12)
	expiresAt := snapshotTime(2026, time.March, 3, 12)
	product := domain.Product{ProductID: "product-1", Name: "Tomato", BaseUnit: domain.BaseUnitG}
	delivery := domain.Delivery{DeliveryID: "delivery-1", Number: "invoice-1", CreatedAt: receivedAt, Lines: []domain.DeliveryLine{{ProductID: "product-1", ManufacturedAt: manufacturedAt, ShelfLife: domain.ShelfLife{Value: 3, Unit: domain.ShelfLifeDay}, ReceivedAt: receivedAt, Quantity: 1000}}}
	batch := domain.Batch{BatchID: "batch-1", DeliveryID: "delivery-1", LineNo: 1, ProductID: "product-1", ManufacturedAt: manufacturedAt, ShelfLife: domain.ShelfLife{Value: 3, Unit: domain.ShelfLifeDay}, ReceivedAt: receivedAt, ExpiresAt: expiresAt, ReceivedQuantity: 1000, RemainingQuantity: remaining, WrittenOffForExpiry: writtenOff}
	expiry := domain.ExpiryResult{BatchID: "batch-1", ProductID: "product-1", WrittenOffQuantity: writtenOff, Reason: domain.ExpiryReasonExpiration, ExpiresAt: expiresAt, ProcessedAt: processedAt}
	snapshot := validSnapshot(3)
	snapshot.State.Products[product.ProductID] = product
	snapshot.State.Deliveries[delivery.DeliveryID] = delivery
	snapshot.State.Batches[batch.BatchID] = batch
	snapshot.State.Commands["register-product"] = domain.CommandRecord{CommandID: "register-product", CommandType: "RegisterProduct", CommandFingerprint: "product-fingerprint", Result: domain.CommandResult{Kind: domain.CommandResultProductRegistered, Product: &product}}
	snapshot.State.Commands["receive-delivery"] = domain.CommandRecord{CommandID: "receive-delivery", CommandType: "ReceiveDelivery", CommandFingerprint: "delivery-fingerprint", Result: domain.CommandResult{Kind: domain.CommandResultDeliveryReceived, Delivery: &domain.DeliveryReceipt{DeliveryID: delivery.DeliveryID, BatchIDs: []string{batch.BatchID}}}}
	snapshot.State.Commands["expire-batch"] = domain.CommandRecord{CommandID: "expire-batch", CommandType: "ExpireBatch", CommandFingerprint: "expiry-fingerprint", Result: domain.CommandResult{Kind: domain.CommandResultBatchExpired, Expiry: &expiry}}
	return snapshot
}

func snapshotTime(year int, month time.Month, day, hour int) time.Time {
	return time.Date(year, month, day, hour, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
}

func productSnapshot() domain.Snapshot {
	snapshot := validSnapshot(1)
	snapshot.State.Products["product-1"] = domain.Product{ProductID: "product-1", Name: "Tomato", BaseUnit: domain.BaseUnitG}
	snapshot.State.Commands["command-1"] = productCommand("command-1", "product-1")
	return snapshot
}

func validSnapshot(sequence int64) domain.Snapshot {
	return domain.Snapshot{
		SchemaVersion: domain.InventorySchemaV1,
		StreamID:      domain.InventoryStreamID,
		LastSequence:  sequence,
		State: domain.InventoryState{
			Products:         map[string]domain.Product{},
			Dishes:           map[string]domain.Dish{},
			Deliveries:       map[string]domain.Delivery{},
			Batches:          map[string]domain.Batch{},
			Orders:           map[string]domain.Order{},
			OrdersByProposal: map[string]string{},
			Commands:         map[string]domain.CommandRecord{},
			StateVersion:     sequence,
		},
	}
}

func productCommand(commandID, productID string) domain.CommandRecord {
	product := &domain.Product{ProductID: productID, Name: "Tomato", BaseUnit: domain.BaseUnitG}
	return domain.CommandRecord{
		CommandID: commandID, CommandType: "RegisterProduct", CommandFingerprint: commandID + "-fingerprint",
		Result: domain.CommandResult{Kind: domain.CommandResultProductRegistered, Product: product},
	}
}

func dishCommand(commandID, dishID, productID string) domain.CommandRecord {
	dish := &domain.Dish{DishID: dishID, Name: "Soup", Ingredients: []domain.Ingredient{{ProductID: productID, Quantity: 500}}}
	return domain.CommandRecord{
		CommandID: commandID, CommandType: "DefineDish", CommandFingerprint: commandID + "-fingerprint",
		Result: domain.CommandResult{Kind: domain.CommandResultDishDefined, Dish: dish},
	}
}
