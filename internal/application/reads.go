package application

import (
	"context"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

// Head is how far the journal has gone and when that was read.
type Head struct {
	StateVersion int64
	EvaluatedAt  time.Time
}

// Head reads the journal's version and the clock without reading the journal.
func (o *Owner) Head(ctx context.Context) (Head, error) {
	version, err := o.events.Version(ctx)
	if err != nil {
		return Head{}, err
	}
	return Head{StateVersion: version, EvaluatedAt: domain.CanonicalBusinessTime(o.now())}, nil
}

// FindOrderPlacement returns where the journal recorded an order.
func (o *Owner) FindOrderPlacement(ctx context.Context, orderID string) (OrderPlacement, bool, error) {
	return o.events.FindOrderPlacement(ctx, orderID)
}

// FindOrder returns an order from the event that placed it.
func (o *Owner) FindOrder(ctx context.Context, orderID string) (contract.Order, bool, error) {
	placement, found, err := o.events.FindOrderPlacement(ctx, orderID)
	if err != nil || !found {
		return contract.Order{}, false, err
	}
	events, err := o.events.ReadEvents(ctx, placement.Sequence-1, 1)
	if err != nil {
		return contract.Order{}, false, err
	}
	if len(events) != 1 || events[0].Sequence != placement.Sequence || events[0].Type != domain.EventOrderPlaced || events[0].Payload.Order == nil || events[0].Payload.Order.OrderID != orderID {
		return contract.Order{}, false, domain.ErrInvalidRecordedBatch
	}
	return toContractOrder(*events[0].Payload.Order), true, nil
}

// ReadHistoryPage returns at most limit events after fromSequence, with the
// version the journal had when the page was read.
func (o *Owner) ReadHistoryPage(ctx context.Context, fromSequence int64, limit int) (contract.History, error) {
	version, err := o.events.Version(ctx)
	if err != nil {
		return contract.History{}, err
	}
	events, err := o.events.ReadEvents(ctx, fromSequence, limit)
	if err != nil {
		return contract.History{}, err
	}
	items := make([]contract.Event, 0, len(events))
	for _, event := range events {
		// An event recorded after the version was read belongs to the next page.
		if event.Sequence > version {
			break
		}
		items = append(items, toContractEvent(event))
	}
	return contract.History{EvaluatedAt: domain.CanonicalBusinessTime(o.now()), StateVersion: version, Events: items}, nil
}

// readPeriod reads the events of a period; an empty journal has none.
func readPeriod(ctx context.Context, events EventStore, query PeriodQuery) ([]domain.Event, error) {
	if query.UpToSequence <= 0 {
		return nil, ctx.Err()
	}
	return events.ReadPeriod(ctx, query)
}

// checkEventSchemas fails on an event of a schema this version does not know.
func checkEventSchemas(events []domain.Event) error {
	for _, event := range events {
		if event.SchemaVersion != domain.InventorySchemaV1 && event.SchemaVersion != domain.InventorySchemaV2 {
			return domain.ErrUnsupportedSchema
		}
	}
	return nil
}
