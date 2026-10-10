package application

import (
	"context"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type EventStore interface {
	AppendBatch(context.Context, domain.CommandBatch) (domain.AppendResult, error)
	ReadBatches(context.Context, int64) ([]domain.RecordedBatch, error)
	FindCommand(context.Context, string) (domain.CommandRecord, bool, error)
	FindOrderByProposal(context.Context, string) (domain.Order, bool, error)
	Version(context.Context) (int64, error)
}

type SnapshotStore interface {
	LoadLatest(context.Context) (domain.Snapshot, bool, error)
	Save(context.Context, domain.Snapshot) error
}
