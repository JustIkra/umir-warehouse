package domain

import (
	"errors"
	"testing"
	"time"
)

func TestReplayWithoutDecisions(t *testing.T) {
	moscow := time.FixedZone("MSK", 3*60*60)
	manufacturedAt := time.Date(2026, time.January, 1, 9, 0, 0, 0, moscow)
	expiresAt := time.Date(2026, time.March, 1, 9, 0, 0, 0, moscow)
	receivedAt := time.Date(2026, time.January, 2, 10, 0, 0, 0, moscow)
	state := InventoryState{
		Products:     map[string]Product{"p1": {ProductID: "p1", Name: "Tomato", BaseUnit: BaseUnitG}},
		StateVersion: 1,
	}
	err := state.ApplyBatch(RecordedBatch{
		CommandID: "receive-d", CommandType: "ReceiveDelivery", CommandFingerprint: "fp-d", FinalSequence: 2,
		Events: []Event{{
			EventID: "delivery-event", StreamID: "inventory", Sequence: 2,
			SchemaVersion: 1, CommandID: "receive-d", CommandFingerprint: "fp-d",
			RecordedAt: receivedAt, Type: EventDeliveryReceived,
			Payload: EventPayload{
				Delivery: &Delivery{DeliveryID: "delivery-1", Number: "invoice-1", CreatedAt: receivedAt, Lines: []DeliveryLine{{ProductID: "p1", ManufacturedAt: manufacturedAt, ShelfLife: ShelfLife{Value: 2, Unit: ShelfLifeMonth}, ReceivedAt: receivedAt, Quantity: 5000}}},
				Batches: []Batch{{
					BatchID: "batch-1", DeliveryID: "delivery-1", LineNo: 1, ProductID: "p1",
					ManufacturedAt: manufacturedAt, ShelfLife: ShelfLife{Value: 2, Unit: ShelfLifeMonth},
					ReceivedAt: receivedAt, ExpiresAt: expiresAt, ReceivedQuantity: 5000, RemainingQuantity: 5000,
				}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.StateVersion != 2 || state.Batches["batch-1"].ExpiresAt != expiresAt {
		t.Fatalf("replayed state = %#v; want version 2 and saved expiry %s", state, expiresAt)
	}
	if _, ok := state.DeliveryIDs["delivery-1"]; !ok {
		t.Fatalf("replayed delivery IDs = %v; want delivery-1", state.DeliveryIDs)
	}
	if state.Batches["batch-1"].RemainingQuantity != 5000 {
		t.Fatalf("replayed quantity = %d; want saved quantity 5000", state.Batches["batch-1"].RemainingQuantity)
	}
	if err := state.ApplyBatch(RecordedBatch{FinalSequence: 2, Events: []Event{}}); !errors.Is(err, ErrInvalidEventBatch) {
		t.Fatalf("empty batch error = %v; want ErrInvalidEventBatch", err)
	}
}

func TestReducerRejectsBrokenReferencesWithoutMutatingInput(t *testing.T) {
	original := InventoryState{StateVersion: 0, Products: map[string]Product{"p1": {ProductID: "p1", Name: "Tomato", BaseUnit: BaseUnitG}}}
	err := original.ApplyBatch(RecordedBatch{
		CommandID: "define-dish", CommandType: "DefineDish", CommandFingerprint: "fp", FinalSequence: 1,
		Events: []Event{{
			EventID: "dish-event", StreamID: "inventory", Sequence: 1, SchemaVersion: 1,
			CommandID: "define-dish", CommandFingerprint: "fp", RecordedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60)),
			Type: EventDishDefined, Payload: EventPayload{Dish: &Dish{DishID: "d1", Name: "Soup", Ingredients: []Ingredient{{ProductID: "missing", Quantity: 1000}}}},
		}},
	})
	if !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("ApplyBatch() error = %v; want ErrProductNotFound", err)
	}
	if original.StateVersion != 0 || len(original.Dishes) != 0 || original.Products["p1"].Name != "Tomato" {
		t.Fatalf("failed replay mutated input state: %#v", original)
	}
}

func TestReducerRejectsRepeatedRecipeProductWithoutMutatingInput(t *testing.T) {
	recordedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	original := InventoryState{
		Products: map[string]Product{"p1": {ProductID: "p1", Name: "Tomato", BaseUnit: BaseUnitG}},
	}
	err := original.ApplyBatch(RecordedBatch{
		CommandID: "define-dish", CommandType: "DefineDish", CommandFingerprint: "fp", FinalSequence: 1,
		Events: []Event{{
			EventID: "dish-event", StreamID: InventoryStreamID, Sequence: 1, SchemaVersion: InventorySchemaV1,
			CommandID: "define-dish", CommandFingerprint: "fp", RecordedAt: recordedAt,
			Type: EventDishDefined, Payload: EventPayload{Dish: &Dish{DishID: "d1", Name: "Soup", Ingredients: []Ingredient{
				{ProductID: "p1", Quantity: 1000},
				{ProductID: "p1", Quantity: 500},
			}}},
		}},
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("ApplyBatch() error = %v; want ErrInvalidEvent", err)
	}
	if original.StateVersion != 0 || len(original.Dishes) != 0 || len(original.Products) != 1 || original.Products["p1"].Name != "Tomato" {
		t.Fatalf("failed replay mutated input state: %#v", original)
	}
}

func TestReducerRejectsUnsupportedSchema(t *testing.T) {
	var state InventoryState
	err := state.ApplyBatch(RecordedBatch{
		CommandID: "register-p", CommandType: "RegisterProduct", CommandFingerprint: "fp", FinalSequence: 1,
		Events: []Event{{
			EventID: "product-event", StreamID: InventoryStreamID, Sequence: 1, SchemaVersion: 2,
			CommandID: "register-p", CommandFingerprint: "fp", RecordedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60)),
			Type: EventProductRegistered, Payload: EventPayload{Product: &Product{ProductID: "p1", Name: "Tomato", BaseUnit: BaseUnitG}},
		}},
	})
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("ApplyBatch() error = %v; want ErrUnsupportedSchema", err)
	}
}

func TestReducerRejectsUnbalancedExpiry(t *testing.T) {
	expiresAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	state := InventoryState{
		Batches: map[string]Batch{"batch-1": {BatchID: "batch-1", ProductID: "p1", ExpiresAt: expiresAt, ReceivedQuantity: 1000, RemainingQuantity: 1000}},
	}
	err := state.ApplyBatch(RecordedBatch{
		CommandID: "expire-1", CommandType: "ExpireBatch", CommandFingerprint: "fp", FinalSequence: 1,
		Events: []Event{{
			EventID: "expiry-event", StreamID: InventoryStreamID, Sequence: 1, SchemaVersion: InventorySchemaV1,
			CommandID: "expire-1", CommandFingerprint: "fp", RecordedAt: expiresAt,
			Type: EventBatchExpired, Payload: EventPayload{Expiry: &ExpiryResult{BatchID: "batch-1", ProductID: "p1", WrittenOffQuantity: 999, Reason: ExpiryReasonExpiration, ExpiresAt: expiresAt, ProcessedAt: expiresAt}},
		}},
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("ApplyBatch() error = %v; want ErrInvalidEvent", err)
	}
	if state.Batches["batch-1"].RemainingQuantity != 1000 || state.Batches["batch-1"].WrittenOffForExpiry != 0 {
		t.Fatalf("failed replay mutated batch: %#v", state.Batches["batch-1"])
	}
}

func TestCanonicalBusinessTimeDropsMonotonicData(t *testing.T) {
	moscow := time.FixedZone("MSK", 3*60*60)
	withMonotonic := time.Now().In(moscow)
	withoutMonotonic := withMonotonic.Round(0)
	if got, want := CanonicalBusinessTimeString(withMonotonic), CanonicalBusinessTimeString(withoutMonotonic); got != want {
		t.Fatalf("canonical time with monotonic data = %q; without = %q", got, want)
	}
	zone, offset := CanonicalBusinessTime(withMonotonic).Zone()
	if zone != "MSK" || offset != 3*60*60 {
		t.Fatalf("canonical zone = %q/%d; want MSK/+03:00", zone, offset)
	}
}
