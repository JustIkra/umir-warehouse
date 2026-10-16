package application

import (
	"context"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type EventStore interface {
	AppendBatch(context.Context, domain.CommandBatch) (domain.AppendResult, error)
	ReadBatches(context.Context, int64) ([]domain.RecordedBatch, error)
	// ScanBatches streams batches after the batch boundary `after` without
	// holding store locks while fn runs. fn gets a copy; its error stops the scan.
	ScanBatches(ctx context.Context, after int64, fn func(domain.RecordedBatch) error) error
	// ReadEvents returns at most limit events with Sequence > after in order.
	ReadEvents(ctx context.Context, after int64, limit int) ([]domain.Event, error)
	ReadPeriod(context.Context, PeriodQuery) ([]domain.Event, error)
	FindOrderPlacement(ctx context.Context, orderID string) (OrderPlacement, bool, error)
	FindCommand(context.Context, string) (domain.CommandRecord, bool, error)
	FindOrderByProposal(context.Context, string) (domain.Order, bool, error)
	Version(context.Context) (int64, error)
}

// The query types live in domain because the in-memory adapter, imported by
// package-internal application tests, cannot import application.
type (
	PeriodQuery    = domain.PeriodQuery
	OrderPlacement = domain.OrderPlacement
)

type SnapshotStore interface {
	LoadLatest(context.Context) (domain.Snapshot, bool, error)
	Save(context.Context, domain.Snapshot) error
}
