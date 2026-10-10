package memory

import (
	"context"
	"sync"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type SnapshotStore struct {
	mu        sync.RWMutex
	snapshot  domain.Snapshot
	populated bool
}

func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{}
}

func (s *SnapshotStore) LoadLatest(ctx context.Context) (domain.Snapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Snapshot{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return domain.Snapshot{}, false, err
	}
	if !s.populated {
		return domain.Snapshot{}, false, nil
	}
	snapshot := domain.CloneSnapshot(s.snapshot)
	if err := ctx.Err(); err != nil {
		return domain.Snapshot{}, false, err
	}
	return snapshot, true, nil
}

func (s *SnapshotStore) Save(ctx context.Context, candidate domain.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	candidate = domain.CloneSnapshot(candidate)
	if err := domain.ValidateSnapshot(candidate); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.populated && candidate.LastSequence <= s.snapshot.LastSequence {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.snapshot = candidate
	s.populated = true
	return nil
}
