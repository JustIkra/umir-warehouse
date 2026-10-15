package domain

import (
	"errors"
	"math"
	"strconv"
	"testing"
	"time"
)

var layerTime = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))

// layerBatch wraps events into a recorded batch continuing from firstSequence.
func layerBatch(commandID, commandType string, firstSequence int64, events ...Event) RecordedBatch {
	recorded := make([]Event, len(events))
	for i, event := range events {
		event.StreamID = InventoryStreamID
		event.Sequence = firstSequence + int64(i)
		event.CommandID = commandID
		event.CommandFingerprint = "fp-" + commandID
		event.SchemaVersion = EventSchemaVersion(event)
		if event.RecordedAt.IsZero() {
			event.RecordedAt = layerTime
		}
		recorded[i] = event
	}
	return RecordedBatch{CommandID: commandID, CommandType: commandType, CommandFingerprint: "fp-" + commandID, Events: recorded, FinalSequence: firstSequence + int64(len(events)) - 1}
}

func layerProductEvent(id string) Event {
	return Event{EventID: "event-" + id, Type: EventProductRegistered, Payload: EventPayload{Product: &Product{ProductID: id, Name: "Product " + id, BaseUnit: BaseUnitG}}}
}

func layerDishEvent(id, productID string) Event {
	return Event{EventID: "event-" + id, Type: EventDishDefined, Payload: EventPayload{Dish: &Dish{DishID: id, Name: "Dish " + id, Ingredients: []Ingredient{{ProductID: productID, Quantity: 1}}}}}
}

func layerDeliveryEvent(deliveryID, productID string, batchIDs ...string) Event {
	manufacturedAt := layerTime.AddDate(0, 0, -1)
	life := ShelfLife{Value: 30, Unit: ShelfLifeDay}
	delivery := Delivery{DeliveryID: deliveryID, Number: "invoice-" + deliveryID, CreatedAt: layerTime}
	batches := make([]Batch, len(batchIDs))
	for i, batchID := range batchIDs {
		delivery.Lines = append(delivery.Lines, DeliveryLine{ProductID: productID, ManufacturedAt: manufacturedAt, ShelfLife: life, ReceivedAt: layerTime, Quantity: 1_000_000_000})
		batches[i] = Batch{BatchID: batchID, DeliveryID: deliveryID, LineNo: int64(i + 1), ProductID: productID, ManufacturedAt: manufacturedAt, ShelfLife: life, ReceivedAt: layerTime, ExpiresAt: manufacturedAt.AddDate(0, 0, 30), ReceivedQuantity: 1_000_000_000, RemainingQuantity: 1_000_000_000}
	}
	return Event{EventID: "event-" + deliveryID, RecordedAt: layerTime, Type: EventDeliveryReceived, Payload: EventPayload{Delivery: &delivery, Batches: batches}}
}

func layerOrderEvent(orderID, dishID, productID, batchID string) Event {
	return Event{EventID: "event-" + orderID, Type: EventOrderPlaced, Payload: EventPayload{Order: &Order{
		OrderID: orderID, ProposalID: "proposal-" + orderID, PlacedAt: layerTime,
		AcceptedPositions: []AcceptedPosition{{LineNo: 1, DishID: dishID, Portions: 1}},
		Allocations:       []Allocation{{ProductID: productID, BatchID: batchID, Quantity: 1}},
	}}}
}

func layerPendingEvent(batchID, productID string, remaining Quantity, expiresAt time.Time) Event {
	return Event{EventID: "event-pending-" + batchID, TenantID: "restaurant", RuntimeEpoch: "epoch", RecordedAt: expiresAt, Type: EventBatchExpiryPending, Payload: EventPayload{Pending: &PendingResult{BatchID: batchID, ProductID: productID, RemainingQuantity: remaining, BaseUnit: BaseUnitG, ExpiresAt: expiresAt}}}
}

// layerState builds a state through the reducer: product p1, dish d1 (1 g of p1),
// one delivery with the given batches and orderCount orders spread over them.
func layerState(t testing.TB, batchCount, orderCount int) InventoryState {
	t.Helper()
	batchIDs := make([]string, batchCount)
	for i := range batchIDs {
		batchIDs[i] = "batch-" + strconv.Itoa(i)
	}
	var state InventoryState
	steps := []RecordedBatch{
		layerBatch("register-p1", "RegisterProduct", 1, layerProductEvent("p1")),
		layerBatch("define-d1", "DefineDish", 2, layerDishEvent("d1", "p1")),
		layerBatch("receive-1", "ReceiveDelivery", 3, layerDeliveryEvent("delivery-1", "p1", batchIDs...)),
	}
	for _, batch := range steps {
		if err := state.ApplyBatch(batch); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < orderCount; i++ {
		orderID := "order-" + strconv.Itoa(i)
		batch := layerBatch("confirm-"+orderID, "ConfirmOrder", state.StateVersion+1, layerOrderEvent(orderID, "d1", "p1", batchIDs[i%batchCount]))
		if err := state.ApplyBatch(batch); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func assertStateUntouched(t *testing.T, before, after InventoryState) {
	t.Helper()
	if !EqualInventoryState(before, after) {
		t.Fatalf("state changed:\nbefore %#v\nafter  %#v", before, after)
	}
	if len(before.Products) != len(after.Products) || len(before.Dishes) != len(after.Dishes) || len(before.DeliveryIDs) != len(after.DeliveryIDs) || len(before.Batches) != len(after.Batches) || before.StateVersion != after.StateVersion {
		t.Fatalf("map sizes or version changed: before %d/%d/%d/%d v%d, after %d/%d/%d/%d v%d",
			len(before.Products), len(before.Dishes), len(before.DeliveryIDs), len(before.Batches), before.StateVersion,
			len(after.Products), len(after.Dishes), len(after.DeliveryIDs), len(after.Batches), after.StateVersion)
	}
}

func TestRejectedBatchLeavesStateUntouched(t *testing.T) {
	for _, c := range []struct {
		name  string
		batch func(version int64) RecordedBatch
		want  error
	}{
		{
			name: "invalid second event",
			batch: func(v int64) RecordedBatch {
				return layerBatch("c", "RegisterProduct", v+1, layerProductEvent("p2"), layerProductEvent("p1"))
			},
			want: ErrInvalidEvent,
		},
		{
			name: "invalid first event",
			batch: func(v int64) RecordedBatch {
				return layerBatch("c", "RegisterProduct", v+1, layerProductEvent("p1"), layerProductEvent("p2"))
			},
			want: ErrInvalidEvent,
		},
		{
			name: "order then unknown dish",
			batch: func(v int64) RecordedBatch {
				return layerBatch("c", "ConfirmOrder", v+1, layerOrderEvent("order-new", "d1", "p1", "batch-0"), layerOrderEvent("order-bad", "missing", "p1", "batch-0"))
			},
			want: ErrDishNotFound,
		},
		{
			name: "delivery then failing event",
			batch: func(v int64) RecordedBatch {
				return layerBatch("c", "ReceiveDelivery", v+1, layerDeliveryEvent("delivery-2", "p1", "batch-new"), layerPendingEvent("missing", "p1", 1, layerTime))
			},
			want: ErrInvalidEvent,
		},
		{
			name: "delivery then duplicate delivery",
			batch: func(v int64) RecordedBatch {
				return layerBatch("c", "ReceiveDelivery", v+1, layerDeliveryEvent("delivery-2", "p1", "batch-new"), layerDeliveryEvent("delivery-2", "p1", "batch-other"))
			},
			want: ErrInvalidEvent,
		},
		{
			name: "sequence gap in second event",
			batch: func(v int64) RecordedBatch {
				batch := layerBatch("c", "RegisterProduct", v+1, layerProductEvent("p2"), layerProductEvent("p3"))
				batch.Events[1].Sequence++
				batch.FinalSequence++
				return batch
			},
			want: ErrInvalidRecordedBatch,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := layerState(t, 3, 5)
			before := CloneInventoryState(state)
			err := state.ApplyBatch(c.batch(state.StateVersion))
			if !errors.Is(err, c.want) {
				t.Fatalf("ApplyBatch() error = %v; want %v", err, c.want)
			}
			assertStateUntouched(t, before, state)

			planned, err := PlanBatch(&state, c.batch(state.StateVersion))
			if !errors.Is(err, c.want) {
				t.Fatalf("PlanBatch() error = %v; want %v", err, c.want)
			}
			if len(planned.Products)+len(planned.Dishes)+len(planned.DeliveryIDs)+len(planned.Batches) != 0 || planned.StateVersion != 0 {
				t.Fatalf("PlanBatch() returned a non-empty delta with an error: %#v", planned)
			}
			assertStateUntouched(t, before, state)
		})
	}
}

func TestPlanBatchDoesNotMutateStateAndMergeAppliesDelta(t *testing.T) {
	state := layerState(t, 2, 0)
	before := CloneInventoryState(state)
	batch := layerBatch("c", "ConfirmOrder", state.StateVersion+1, layerOrderEvent("order-1", "d1", "p1", "batch-0"))
	delta, err := PlanBatch(&state, batch)
	if err != nil {
		t.Fatal(err)
	}
	assertStateUntouched(t, before, state)
	if delta.StateVersion != batch.FinalSequence || len(delta.Batches) != 1 || delta.Batches["batch-0"].RemainingQuantity != 1_000_000_000-1 || delta.Batches["batch-0"].ConsumedByOrders != 1 {
		t.Fatalf("delta = %#v; want only batch-0 consumed by 1 at version %d", delta, batch.FinalSequence)
	}
	state.Merge(delta)
	if state.StateVersion != batch.FinalSequence || state.Batches["batch-0"].RemainingQuantity != 1_000_000_000-1 || state.Batches["batch-1"] != before.Batches["batch-1"] {
		t.Fatalf("merged state = %#v; want batch-0 reduced and batch-1 intact", state)
	}
}

func TestMergeInitializesNilMaps(t *testing.T) {
	var state InventoryState
	state.Merge(StateDelta{Products: map[string]Product{"p": {ProductID: "p"}}, DeliveryIDs: map[string]struct{}{"d": {}}, StateVersion: 4})
	if state.Products["p"].ProductID != "p" || state.Dishes == nil || state.Batches == nil || state.DeliveryIDs == nil || state.StateVersion != 4 {
		t.Fatalf("Merge into a zero state = %#v", state)
	}
	if _, ok := state.DeliveryIDs["d"]; !ok {
		t.Fatal("Merge dropped the delivery ID")
	}
}

func TestPlanBatchLaterEventsSeeEarlierEventsOfSameBatch(t *testing.T) {
	state := layerState(t, 1, 0)
	expiresAt := state.Batches["batch-0"].ExpiresAt
	// A delivery whose batch is already due, followed by the pending event for that very batch.
	delivery := layerDeliveryEvent("delivery-2", "p1", "batch-new")
	delivery.Payload.Batches[0].ExpiresAt = expiresAt
	batch := layerBatch("c", "ReceiveDelivery", state.StateVersion+1, delivery, layerPendingEvent("batch-new", "p1", 1_000_000_000, expiresAt))
	batch.Events[0].RecordedAt = expiresAt
	delta, err := PlanBatch(&state, batch)
	if err != nil {
		t.Fatal(err)
	}
	if !delta.Batches["batch-new"].PendingWriteoff {
		t.Fatalf("pending event did not see the batch created earlier in the same batch: %#v", delta.Batches["batch-new"])
	}
	if _, ok := delta.DeliveryIDs["delivery-2"]; !ok {
		t.Fatalf("delta lost the delivery ID: %#v", delta.DeliveryIDs)
	}
	if _, ok := state.Batches["batch-new"]; ok {
		t.Fatal("PlanBatch wrote the new batch into the state")
	}
}

func TestApplyBatchRejectsDuplicateDeliveryID(t *testing.T) {
	state := layerState(t, 1, 0)
	err := state.ApplyBatch(layerBatch("c", "ReceiveDelivery", state.StateVersion+1, layerDeliveryEvent("delivery-1", "p1", "batch-other")))
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("duplicate delivery ID error = %v; want ErrInvalidEvent", err)
	}
}

func TestValidateInventoryTotalsWithSubstitutesDeltaWithoutCopying(t *testing.T) {
	state := InventoryState{Batches: map[string]Batch{
		"a": {BatchID: "a", ProductID: "p", RemainingQuantity: 5},
		"b": {BatchID: "b", ProductID: "p", RemainingQuantity: 5},
	}}
	if err := ValidateInventoryTotalsWith(&state, StateDelta{}); err != nil {
		t.Fatalf("totals of a valid state error = %v", err)
	}
	overflow := StateDelta{Batches: map[string]Batch{"a": {BatchID: "a", ProductID: "p", RemainingQuantity: Quantity(math.MaxInt64)}}}
	if err := ValidateInventoryTotalsWith(&state, overflow); !errors.Is(err, ErrQuantityOverflow) {
		t.Fatalf("overflowing replacement error = %v; want ErrQuantityOverflow", err)
	}
	negative := StateDelta{Batches: map[string]Batch{"c": {BatchID: "c", ProductID: "p", RemainingQuantity: -1}}}
	if err := ValidateInventoryTotalsWith(&state, negative); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("negative new batch error = %v; want ErrInvalidQuantity", err)
	}
	if state.Batches["a"].RemainingQuantity != 5 || len(state.Batches) != 2 {
		t.Fatalf("ValidateInventoryTotalsWith changed the state: %#v", state.Batches)
	}
}

// The work per applied batch must not depend on how many orders came before:
// two states with the same number of stock batches but 10x the order history
// must cost the same allocations.
func TestApplyBatchAllocsIndependentOfHistory(t *testing.T) {
	const batchCount = 50
	small, large := layerState(t, batchCount, 1000), layerState(t, batchCount, 10_000)
	if len(small.Batches) != len(large.Batches) {
		t.Fatalf("batch counts differ: %d vs %d", len(small.Batches), len(large.Batches))
	}
	allocs := func(state InventoryState) float64 {
		next := 0
		return testing.AllocsPerRun(50, func() {
			orderID := "extra-" + strconv.Itoa(next)
			batch := layerBatch("extra-"+orderID, "ConfirmOrder", state.StateVersion+1, layerOrderEvent(orderID, "d1", "p1", "batch-"+strconv.Itoa(next%batchCount)))
			next++
			if err := state.ApplyBatch(batch); err != nil {
				t.Fatal(err)
			}
		})
	}
	smallAllocs, largeAllocs := allocs(small), allocs(large)
	t.Logf("allocations per applied order: %.0f after 1000 orders, %.0f after 10000 orders", smallAllocs, largeAllocs)
	if math.Abs(largeAllocs-smallAllocs) > 0.1*smallAllocs {
		t.Fatalf("allocations depend on history: %.0f after 1000 orders vs %.0f after 10000 orders (limit 10%%)", smallAllocs, largeAllocs)
	}
}
