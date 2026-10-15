package storecontract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/application"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func RunEventStore(t *testing.T, f Fixture) {
	t.Helper()
	for _, c := range []struct {
		name string
		run  func(*testing.T, Fixture)
	}{
		{"command replay returns the recorded result", eventsCommandReplay},
		{"command identity conflict", eventsIdentityConflict},
		{"expected version", eventsExpectedVersion},
		{"multi-event batch is atomic and sequential", eventsAtomicBatch},
		{"event and proposal IDs are unique", eventsUniqueEventAndProposal},
		{"read batches boundaries", eventsReadBatchesBoundaries},
		{"find command and order by proposal", eventsFindLookups},
		{"canceled context leaves no trace", eventsCanceledAppend},
		{"restaurants are isolated", eventsRestaurantIsolation},
	} {
		t.Run(c.name, func(t *testing.T) { c.run(t, f) })
	}
}

func eventsCommandReplay(t *testing.T, f Fixture) {
	ctx := context.Background()
	events := f.open(t).Events
	first := productBatch("cmd-1", "fp-1", "e-1", "p1", 0)
	if _, err := events.AppendBatch(ctx, first); err != nil {
		t.Fatal(err)
	}
	first.ExpectedVersion = 91
	result, err := events.AppendBatch(ctx, first)
	if err != nil || !result.Replayed {
		t.Fatalf("AppendBatch(same identity) = %#v, %v; want replay, nil", result, err)
	}
	if len(result.Batch.Events) != 1 || result.Batch.Events[0].EventID != "e-1" || result.Command.Result.Product == nil {
		t.Fatalf("replayed result = %#v; want the originally recorded batch and result", result)
	}
	assertVersion(t, events, 1)
}

func eventsIdentityConflict(t *testing.T, f Fixture) {
	ctx := context.Background()
	events := f.open(t).Events
	if _, err := events.AppendBatch(ctx, productBatch("cmd-1", "fp-1", "e-1", "p1", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := events.AppendBatch(ctx, productBatch("cmd-1", "fp-2", "e-2", "p2", 1)); !errors.Is(err, domain.ErrCommandIdentityConflict) {
		t.Fatalf("AppendBatch(other fingerprint) error = %v; want ErrCommandIdentityConflict", err)
	}
	other := productBatch("cmd-1", "fp-1", "e-2", "p2", 1)
	other.CommandType = "RegisterProductAgain"
	if _, err := events.AppendBatch(ctx, other); !errors.Is(err, domain.ErrCommandIdentityConflict) {
		t.Fatalf("AppendBatch(other type) error = %v; want ErrCommandIdentityConflict", err)
	}
	assertVersion(t, events, 1)
}

func eventsExpectedVersion(t *testing.T, f Fixture) {
	ctx := context.Background()
	events := f.open(t).Events
	if _, err := events.AppendBatch(ctx, productBatch("cmd-1", "fp-1", "e-1", "p1", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := events.AppendBatch(ctx, productBatch("cmd-2", "fp-2", "e-2", "p2", 0)); !errors.Is(err, domain.ErrConcurrentModification) {
		t.Fatalf("AppendBatch(stale version) error = %v; want ErrConcurrentModification", err)
	}
	if _, ok, err := events.FindCommand(ctx, "cmd-2"); err != nil || ok {
		t.Fatalf("FindCommand(rejected) = %v, %v; want absent", ok, err)
	}
	assertVersion(t, events, 1)
	result, err := events.AppendBatch(ctx, productBatch("cmd-1", "fp-1", "e-1", "p1", 0))
	if err != nil || !result.Replayed || result.Batch.FinalSequence != 1 {
		t.Fatalf("AppendBatch(recorded command, old version) = %#v, %v; want replay of sequence 1", result, err)
	}
	if _, err := events.AppendBatch(ctx, productBatch("cmd-2", "fp-2", "e-2", "p2", 1)); err != nil {
		t.Fatalf("AppendBatch(current version) error = %v", err)
	}
	assertVersion(t, events, 2)
}

func eventsAtomicBatch(t *testing.T, f Fixture) {
	ctx := context.Background()
	events := f.open(t).Events
	result, err := events.AppendBatch(ctx, deliveryBatch("receive-1", "fp-r1", "delivery-1", "batch-1", "delivery-event-1", 0, true))
	if err != nil {
		t.Fatal(err)
	}
	if result.Batch.FinalSequence != 2 || len(result.Batch.Events) != 2 {
		t.Fatalf("AppendBatch() = %#v; want one batch of two events", result.Batch)
	}
	for i, event := range result.Batch.Events {
		if event.Sequence != int64(i+1) {
			t.Fatalf("event %d sequence = %d; want %d", i, event.Sequence, i+1)
		}
	}
	command, ok, err := events.FindCommand(ctx, "receive-1")
	if err != nil || !ok {
		t.Fatalf("FindCommand() = %#v, %v, %v; want recorded receive command", command, ok, err)
	}
	if command.CommandType != "ReceiveDelivery" || command.Result.Kind != domain.CommandResultDeliveryReceived || command.Result.Delivery == nil ||
		len(command.Result.Delivery.BatchIDs) != 1 || command.Result.Delivery.BatchIDs[0] != "batch-1" {
		t.Fatalf("recorded result = %#v; want DeliveryReceived receipt for batch-1", command.Result)
	}
	next, err := events.AppendBatch(ctx, productBatch("cmd-2", "fp-2", "e-2", "p2", 2))
	if err != nil || next.Batch.Events[0].Sequence != 3 {
		t.Fatalf("AppendBatch(after batch) = %#v, %v; want sequence 3", next, err)
	}

	rejected := deliveryBatch("receive-2", "fp-r2", "delivery-2", "batch-2", "delivery-event-2", 3, true)
	rejected.Events[1].EventID = "e-2"
	if _, err := events.AppendBatch(ctx, rejected); !errors.Is(err, domain.ErrInvalidEventBatch) {
		t.Fatalf("AppendBatch(second event reuses an event ID) error = %v; want ErrInvalidEventBatch", err)
	}
	rejected = deliveryBatch("receive-3", "fp-r3", "delivery-3", "batch-3", "delivery-event-3", 3, true)
	rejected.Events[1].EventID = rejected.Events[0].EventID
	if _, err := events.AppendBatch(ctx, rejected); !errors.Is(err, domain.ErrInvalidEventBatch) {
		t.Fatalf("AppendBatch(duplicate event ID in batch) error = %v; want ErrInvalidEventBatch", err)
	}
	for _, id := range []string{"receive-2", "receive-3"} {
		if _, ok, err := events.FindCommand(ctx, id); err != nil || ok {
			t.Fatalf("FindCommand(%s) = %v, %v; rejected batch must leave no command", id, ok, err)
		}
	}
	assertVersion(t, events, 3)
}

func eventsUniqueEventAndProposal(t *testing.T, f Fixture) {
	ctx := context.Background()
	events := f.open(t).Events
	if _, err := events.AppendBatch(ctx, orderBatch("confirm-1", "order-event-1", "order-1", "proposal-1", 0)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		batch domain.CommandBatch
		want  error
	}{
		{"event ID", productBatch("cmd-2", "fp-2", "order-event-1", "p2", 1), domain.ErrInvalidEventBatch},
		{"proposal ID", orderBatch("confirm-2", "order-event-2", "order-2", "proposal-1", 1), domain.ErrInvalidEvent},
	} {
		if _, err := events.AppendBatch(ctx, c.batch); !errors.Is(err, c.want) {
			t.Fatalf("AppendBatch(duplicate %s) error = %v; want %v", c.name, err, c.want)
		}
		if _, ok, err := events.FindCommand(ctx, c.batch.CommandID); err != nil || ok {
			t.Fatalf("FindCommand(%s) = %v, %v; rejected duplicate %s must leave no command", c.batch.CommandID, ok, err, c.name)
		}
	}
	assertVersion(t, events, 1)
}

func eventsReadBatchesBoundaries(t *testing.T, f Fixture) {
	ctx := context.Background()
	events := f.open(t).Events
	for _, batch := range []domain.CommandBatch{
		productBatch("cmd-1", "fp-1", "e-1", "p1", 0),
		deliveryBatch("receive-1", "fp-r1", "delivery-1", "batch-1", "delivery-event-1", 1, true),
		productBatch("cmd-2", "fp-2", "e-2", "p2", 3),
	} {
		if _, err := events.AppendBatch(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		after int64
		want  []int64
	}{
		{0, []int64{1, 3, 4}},
		{1, []int64{3, 4}},
		{3, []int64{4}},
		{4, nil},
	} {
		batches, err := events.ReadBatches(ctx, c.after)
		if err != nil {
			t.Fatalf("ReadBatches(%d) error = %v", c.after, err)
		}
		if len(batches) != len(c.want) {
			t.Fatalf("ReadBatches(%d) returned %d batches; want %d", c.after, len(batches), len(c.want))
		}
		for i, batch := range batches {
			if batch.FinalSequence != c.want[i] {
				t.Fatalf("ReadBatches(%d)[%d].FinalSequence = %d; want %d", c.after, i, batch.FinalSequence, c.want[i])
			}
		}
	}
	for _, after := range []int64{2, -1} {
		if _, err := events.ReadBatches(ctx, after); !errors.Is(err, domain.ErrInvalidRecordedBatch) {
			t.Fatalf("ReadBatches(%d) error = %v; want ErrInvalidRecordedBatch", after, err)
		}
	}
}

func eventsFindLookups(t *testing.T, f Fixture) {
	ctx := context.Background()
	events := f.open(t).Events
	if _, ok, err := events.FindCommand(ctx, "cmd-1"); err != nil || ok {
		t.Fatalf("FindCommand(unknown) = %v, %v; want absent", ok, err)
	}
	if _, ok, err := events.FindOrderByProposal(ctx, "proposal-1"); err != nil || ok {
		t.Fatalf("FindOrderByProposal(unknown) = %v, %v; want absent", ok, err)
	}
	if _, err := events.AppendBatch(ctx, productBatch("cmd-1", "fp-1", "e-1", "p1", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := events.AppendBatch(ctx, orderBatch("confirm-1", "order-event-1", "order-1", "proposal-1", 1)); err != nil {
		t.Fatal(err)
	}
	command, ok, err := events.FindCommand(ctx, "cmd-1")
	if err != nil || !ok || command.CommandType != "RegisterProduct" || command.CommandFingerprint != "fp-1" ||
		command.Result.Kind != domain.CommandResultProductRegistered || command.Result.Product == nil || command.Result.Product.ProductID != "p1" {
		t.Fatalf("FindCommand() = %#v, %v, %v; want recorded product command", command, ok, err)
	}
	order, ok, err := events.FindOrderByProposal(ctx, "proposal-1")
	if err != nil || !ok || order.OrderID != "order-1" || len(order.Allocations) != 1 || order.Allocations[0].Quantity != 1000 {
		t.Fatalf("FindOrderByProposal() = %#v, %v, %v; want order-1", order, ok, err)
	}
	order.Allocations[0].Quantity = 9
	again, _, err := events.FindOrderByProposal(ctx, "proposal-1")
	if err != nil || again.Allocations[0].Quantity != 1000 {
		t.Fatalf("FindOrderByProposal() after caller mutation = %#v, %v; want stored allocation", again, err)
	}
}

func eventsCanceledAppend(t *testing.T, f Fixture) {
	events := f.open(t).Events
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := events.AppendBatch(ctx, productBatch("cmd-1", "fp-1", "e-1", "p1", 0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("AppendBatch(canceled context) error = %v; want context.Canceled", err)
	}
	assertVersion(t, events, 0)
	if _, ok, err := events.FindCommand(context.Background(), "cmd-1"); err != nil || ok {
		t.Fatalf("FindCommand() = %v, %v; canceled append must leave no command", ok, err)
	}
	if _, err := events.AppendBatch(context.Background(), productBatch("cmd-1", "fp-1", "e-1", "p1", 0)); err != nil {
		t.Fatalf("AppendBatch(same IDs after canceled attempt) error = %v", err)
	}
}

func eventsRestaurantIsolation(t *testing.T, f Fixture) {
	ctx := context.Background()
	first := f.open(t)
	second := f.openOther(t, first)
	if _, err := first.Events.AppendBatch(ctx, orderBatch("confirm-1", "order-event-1", "order-1", "proposal-1", 0)); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, second.Events, 0)
	if _, ok, err := second.Events.FindCommand(ctx, "confirm-1"); err != nil || ok {
		t.Fatalf("FindCommand() in other restaurant = %v, %v; want absent", ok, err)
	}
	if _, ok, err := second.Events.FindOrderByProposal(ctx, "proposal-1"); err != nil || ok {
		t.Fatalf("FindOrderByProposal() in other restaurant = %v, %v; want absent", ok, err)
	}
	if _, err := second.Events.AppendBatch(ctx, orderBatch("confirm-1", "order-event-1", "order-1", "proposal-1", 0)); err != nil {
		t.Fatalf("AppendBatch(same IDs in other restaurant) error = %v", err)
	}
	if _, err := second.Events.AppendBatch(ctx, productBatch("cmd-2", "fp-2", "e-2", "p2", 1)); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, first.Events, 1)
	assertVersion(t, second.Events, 2)
	batches, err := first.Events.ReadBatches(ctx, 0)
	if err != nil || len(batches) != 1 {
		t.Fatalf("ReadBatches() = %#v, %v; want only the first restaurant's batch", batches, err)
	}
}

func assertVersion(t *testing.T, events application.EventStore, want int64) {
	t.Helper()
	if got, err := events.Version(context.Background()); err != nil || got != want {
		t.Fatalf("Version() = %d, %v; want %d, nil", got, err, want)
	}
}

func moscowTime(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
}

func productBatch(commandID, fingerprint, eventID, productID string, expected int64) domain.CommandBatch {
	return domain.CommandBatch{
		ExpectedVersion: expected, CommandID: commandID, CommandType: "RegisterProduct", CommandFingerprint: fingerprint,
		Events: []domain.Event{{
			EventID: eventID, RecordedAt: moscowTime(2026, time.March, 1), Type: domain.EventProductRegistered,
			Payload: domain.EventPayload{Product: &domain.Product{ProductID: productID, Name: "Tomato", BaseUnit: domain.BaseUnitG}},
		}},
	}
}

func orderBatch(commandID, eventID, orderID, proposalID string, expected int64) domain.CommandBatch {
	order := &domain.Order{
		OrderID: orderID, ProposalID: proposalID, PlacedAt: moscowTime(2026, time.March, 1),
		AcceptedPositions: []domain.AcceptedPosition{{LineNo: 1, DishID: "d1", Portions: 1}},
		Allocations:       []domain.Allocation{{ProductID: "p1", BatchID: "batch-1", Quantity: 1000}},
	}
	return domain.CommandBatch{
		ExpectedVersion: expected, CommandID: commandID, CommandType: "ConfirmOrder", CommandFingerprint: commandID + "-fingerprint",
		Events: []domain.Event{{EventID: eventID, RecordedAt: moscowTime(2026, time.March, 1), Type: domain.EventOrderPlaced, Payload: domain.EventPayload{Order: order}}},
	}
}

// deliveryBatch builds a ReceiveDelivery command; withExpiry adds a BatchExpired
// event named eventID+"-expired" in the same batch.
func deliveryBatch(commandID, fingerprint, deliveryID, batchID, eventID string, expected int64, withExpiry bool) domain.CommandBatch {
	at := moscowTime(2026, time.March, 1)
	batch := domain.CommandBatch{
		ExpectedVersion: expected, CommandID: commandID, CommandType: "ReceiveDelivery", CommandFingerprint: fingerprint,
		Events: []domain.Event{{EventID: eventID, RecordedAt: at, Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{
			Delivery: &domain.Delivery{DeliveryID: deliveryID, Number: "invoice-" + deliveryID, Lines: []domain.DeliveryLine{{ProductID: "p1", Quantity: 1000}}},
			Batches:  []domain.Batch{{BatchID: batchID, DeliveryID: deliveryID, ProductID: "p1", ExpiresAt: at, ReceivedQuantity: 1000, RemainingQuantity: 1000}},
		}}},
	}
	if withExpiry {
		batch.Events = append(batch.Events, domain.Event{EventID: eventID + "-expired", RecordedAt: at, Type: domain.EventBatchExpired, Payload: domain.EventPayload{
			Expiry: &domain.ExpiryResult{BatchID: batchID, ProductID: "p1", WrittenOffQuantity: 1000, Reason: domain.ExpiryReasonExpiration, ExpiresAt: at, ProcessedAt: at},
		}})
	}
	return batch
}
