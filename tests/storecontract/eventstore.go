package storecontract

import (
	"context"
	"errors"
	"slices"
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
		{"order and delivery IDs are unique", eventsUniqueOrderAndDelivery},
		{"read batches boundaries", eventsReadBatchesBoundaries},
		{"scan batches boundaries and callback error", eventsScanBatches},
		{"read events window", eventsReadEvents},
		{"read period", eventsReadPeriod},
		{"find order placement", eventsFindOrderPlacement},
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

func eventsUniqueOrderAndDelivery(t *testing.T, f Fixture) {
	for _, c := range []struct {
		name  string
		batch domain.CommandBatch
	}{
		{"order ID", orderBatch("confirm-2", "order-event-2", "order-1", "proposal-2", 2)},
		{"delivery ID", deliveryBatch("receive-2", "fp-r2", "delivery-1", "batch-2", "delivery-event-2", 2, false)},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			events := f.open(t).Events
			if _, err := events.AppendBatch(ctx, orderBatch("confirm-1", "order-event-1", "order-1", "proposal-1", 0)); err != nil {
				t.Fatal(err)
			}
			if _, err := events.AppendBatch(ctx, deliveryBatch("receive-1", "fp-r1", "delivery-1", "batch-1", "delivery-event-1", 1, false)); err != nil {
				t.Fatal(err)
			}
			if _, err := events.AppendBatch(ctx, c.batch); !errors.Is(err, domain.ErrInvalidEvent) {
				t.Fatalf("AppendBatch(duplicate %s) error = %v; want ErrInvalidEvent", c.name, err)
			}
			if _, ok, err := events.FindCommand(ctx, c.batch.CommandID); err != nil || ok {
				t.Fatalf("FindCommand(%s) = %v, %v; rejected duplicate must leave no command", c.batch.CommandID, ok, err)
			}
			assertVersion(t, events, 2)
		})
	}
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

// appendTimeline records product (sequence 1), a delivery with expiry
// (2, 3), an order (4) and a product (5), each command on its own day: March 1..4, 12:00 MSK.
func appendTimeline(t *testing.T, events application.EventStore) {
	t.Helper()
	for day, batch := range []domain.CommandBatch{
		productBatch("cmd-1", "fp-1", "e-1", "p1", 0),
		deliveryBatch("receive-1", "fp-r1", "delivery-1", "batch-1", "delivery-event-1", 1, true),
		orderBatch("confirm-1", "order-event-1", "order-1", "proposal-1", 3),
		productBatch("cmd-2", "fp-2", "e-2", "p2", 4),
	} {
		if _, err := events.AppendBatch(context.Background(), atDay(batch, day+1)); err != nil {
			t.Fatal(err)
		}
	}
}

func eventsScanBatches(t *testing.T, f Fixture) {
	ctx := context.Background()
	events := f.open(t).Events
	appendTimeline(t, events)
	for _, c := range []struct {
		after int64
		want  []int64
	}{
		{0, []int64{1, 3, 4, 5}},
		{1, []int64{3, 4, 5}},
		{4, []int64{5}},
		{5, nil},
	} {
		var got []int64
		err := events.ScanBatches(ctx, c.after, func(batch domain.RecordedBatch) error {
			got = append(got, batch.FinalSequence)
			return nil
		})
		if err != nil || !slices.Equal(got, c.want) {
			t.Fatalf("ScanBatches(%d) = %v, %v; want %v, nil", c.after, got, err, c.want)
		}
	}
	for _, after := range []int64{2, -1} {
		called := false
		err := events.ScanBatches(ctx, after, func(domain.RecordedBatch) error { called = true; return nil })
		if !errors.Is(err, domain.ErrInvalidRecordedBatch) || called {
			t.Fatalf("ScanBatches(%d) error = %v, called = %v; want ErrInvalidRecordedBatch without callbacks", after, err, called)
		}
	}

	stop := errors.New("stop")
	calls := 0
	err := events.ScanBatches(ctx, 0, func(domain.RecordedBatch) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("ScanBatches(callback error) = %v after %d calls; want the callback error after 1 call", err, calls)
	}

	err = events.ScanBatches(ctx, 0, func(batch domain.RecordedBatch) error {
		batch.Events[0].EventID = "mutated"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	batches, err := events.ReadBatches(ctx, 0)
	if err != nil || batches[0].Events[0].EventID != "e-1" {
		t.Fatalf("ReadBatches() after callback mutation = %#v, %v; want stored events", batches, err)
	}

	// The store must not be locked while the callback runs.
	err = events.ScanBatches(ctx, 0, func(domain.RecordedBatch) error {
		_, err := events.Version(ctx)
		return err
	})
	if err != nil {
		t.Fatalf("Version() inside ScanBatches callback error = %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	calls = 0
	err = events.ScanBatches(canceled, 0, func(domain.RecordedBatch) error {
		calls++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("ScanBatches(canceled in callback) = %v after %d calls; want context.Canceled after 1 call", err, calls)
	}
}

func eventsReadEvents(t *testing.T, f Fixture) {
	ctx := context.Background()
	events := f.open(t).Events
	appendTimeline(t, events)
	for _, c := range []struct {
		after int64
		limit int
		want  []int64
	}{
		{0, 100, []int64{1, 2, 3, 4, 5}},
		{0, 2, []int64{1, 2}},
		{1, 1, []int64{2}},
		{2, 2, []int64{3, 4}},
		{4, 10, []int64{5}},
		{5, 10, nil},
		{9, 10, nil},
	} {
		got, err := events.ReadEvents(ctx, c.after, c.limit)
		if err != nil || !slices.Equal(sequences(got), c.want) {
			t.Fatalf("ReadEvents(%d, %d) = %v, %v; want %v, nil", c.after, c.limit, sequences(got), err, c.want)
		}
	}
	for _, c := range []struct {
		after int64
		limit int
	}{{0, 0}, {0, -1}, {-1, 10}} {
		if _, err := events.ReadEvents(ctx, c.after, c.limit); !errors.Is(err, domain.ErrInvalidRecordedBatch) {
			t.Fatalf("ReadEvents(%d, %d) error = %v; want ErrInvalidRecordedBatch", c.after, c.limit, err)
		}
	}
	got, err := events.ReadEvents(ctx, 0, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("ReadEvents() = %v, %v", got, err)
	}
	got[0].EventID = "mutated"
	again, err := events.ReadEvents(ctx, 0, 1)
	if err != nil || again[0].EventID != "e-1" {
		t.Fatalf("ReadEvents() after caller mutation = %#v, %v; want stored event", again, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := events.ReadEvents(canceled, 0, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadEvents(canceled) error = %v; want context.Canceled", err)
	}
}

func eventsReadPeriod(t *testing.T, f Fixture) {
	ctx := context.Background()
	events := f.open(t).Events
	appendTimeline(t, events)
	day := func(d int) time.Time { return moscowTime(2026, time.March, d) }
	for _, c := range []struct {
		name string
		q    application.PeriodQuery
		want []int64
	}{
		{"all", application.PeriodQuery{From: day(1), To: day(5), UpToSequence: 5}, []int64{1, 2, 3, 4, 5}},
		{"from is inclusive", application.PeriodQuery{From: day(2), To: day(5), UpToSequence: 5}, []int64{2, 3, 4, 5}},
		{"to is exclusive", application.PeriodQuery{From: day(1), To: day(3), UpToSequence: 5}, []int64{1, 2, 3}},
		{"single instant window", application.PeriodQuery{From: day(2), To: day(2).Add(time.Nanosecond), UpToSequence: 5}, []int64{2, 3}},
		{"empty window", application.PeriodQuery{From: day(2), To: day(2), UpToSequence: 5}, nil},
		{"instant compared across zones", application.PeriodQuery{From: day(2).UTC(), To: day(3).UTC(), UpToSequence: 5}, []int64{2, 3}},
		{"types", application.PeriodQuery{From: day(1), To: day(5), UpToSequence: 5, Types: []domain.EventType{domain.EventBatchExpired, domain.EventOrderPlaced}}, []int64{3, 4}},
		{"type outside window", application.PeriodQuery{From: day(1), To: day(2), UpToSequence: 5, Types: []domain.EventType{domain.EventOrderPlaced}}, nil},
		{"up to sequence inside a batch", application.PeriodQuery{From: day(1), To: day(5), UpToSequence: 2}, []int64{1, 2}},
		{"up to sequence and window", application.PeriodQuery{From: day(2), To: day(5), UpToSequence: 4}, []int64{2, 3, 4}},
		{"up to sequence beyond the head", application.PeriodQuery{From: day(1), To: day(5), UpToSequence: 99}, []int64{1, 2, 3, 4, 5}},
	} {
		got, err := events.ReadPeriod(ctx, c.q)
		if err != nil || !slices.Equal(sequences(got), c.want) {
			t.Fatalf("ReadPeriod(%s) = %v, %v; want %v, nil", c.name, sequences(got), err, c.want)
		}
	}
	for _, upTo := range []int64{0, -1} {
		q := application.PeriodQuery{From: day(1), To: day(5), UpToSequence: upTo}
		if _, err := events.ReadPeriod(ctx, q); !errors.Is(err, domain.ErrInvalidRecordedBatch) {
			t.Fatalf("ReadPeriod(UpToSequence=%d) error = %v; want ErrInvalidRecordedBatch", upTo, err)
		}
	}
	q := application.PeriodQuery{From: day(1), To: day(2), UpToSequence: 5}
	got, err := events.ReadPeriod(ctx, q)
	if err != nil || len(got) != 1 {
		t.Fatalf("ReadPeriod() = %v, %v", got, err)
	}
	got[0].Payload.Product.Name = "mutated"
	again, err := events.ReadPeriod(ctx, q)
	if err != nil || again[0].Payload.Product.Name != "Tomato" {
		t.Fatalf("ReadPeriod() after caller mutation = %#v, %v; want stored product", again, err)
	}
}

func eventsFindOrderPlacement(t *testing.T, f Fixture) {
	ctx := context.Background()
	events := f.open(t).Events
	if _, ok, err := events.FindOrderPlacement(ctx, "order-1"); err != nil || ok {
		t.Fatalf("FindOrderPlacement(empty store) = %v, %v; want absent", ok, err)
	}
	appendTimeline(t, events)
	placement, ok, err := events.FindOrderPlacement(ctx, "order-1")
	if err != nil || !ok || placement.Sequence != 4 || !placement.RecordedAt.Equal(moscowTime(2026, time.March, 3)) {
		t.Fatalf("FindOrderPlacement(order-1) = %#v, %v, %v; want sequence 4 at March 3 12:00 MSK", placement, ok, err)
	}
	if _, offset := placement.RecordedAt.Zone(); offset != 3*60*60 {
		t.Fatalf("FindOrderPlacement(order-1).RecordedAt zone offset = %d; want MSK as recorded", offset)
	}
	if _, ok, err := events.FindOrderPlacement(ctx, "unknown"); err != nil || ok {
		t.Fatalf("FindOrderPlacement(unknown) = %v, %v; want absent", ok, err)
	}
	other := f.openOther(t, f.open(t))
	if _, ok, err := other.Events.FindOrderPlacement(ctx, "order-1"); err != nil || ok {
		t.Fatalf("FindOrderPlacement() in other restaurant = %v, %v; want absent", ok, err)
	}
}

func sequences(events []domain.Event) []int64 {
	var result []int64
	for _, event := range events {
		result = append(result, event.Sequence)
	}
	return result
}

// atDay moves the batch to 12:00 MSK on the given day of March 2026, keeping
// the business time inside the payloads equal to the recorded time.
func atDay(batch domain.CommandBatch, day int) domain.CommandBatch {
	at := moscowTime(2026, time.March, day)
	for i := range batch.Events {
		event := &batch.Events[i]
		event.RecordedAt = at
		if event.Payload.Order != nil {
			event.Payload.Order.PlacedAt = at
		}
		if event.Payload.Expiry != nil {
			event.Payload.Expiry.ProcessedAt = at
		}
	}
	return batch
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
