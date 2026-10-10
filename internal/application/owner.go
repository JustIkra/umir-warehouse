package application

import (
	"context"
	"sync"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

var ErrNotDue = contract.ErrNotDue

type Owner struct {
	mu          sync.Mutex
	events      EventStore
	now         func() time.Time
	newID       func() string
	state       domain.InventoryState
	expirations map[string]time.Time
	usable      bool
	pending     *domain.CommandBatch
}

type ReadState struct {
	State        domain.InventoryState
	EvaluatedAt  time.Time
	StateVersion int64
}

func NewOwner(events EventStore, now func() time.Time, newID func() string) (*Owner, error) {
	if events == nil || now == nil || newID == nil {
		return nil, contract.ErrInvalidArgument
	}
	return &Owner{events: events, now: now, newID: newID}, nil
}

func (o *Owner) Recover(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.recoverLocked(ctx); err != nil {
		return inventoryUnavailable(err)
	}
	if o.pending != nil {
		if err := o.resolvePendingLocked(ctx); err != nil {
			return inventoryUnavailable(err)
		}
	}
	return nil
}

func (o *Owner) Prepare(ctx context.Context) (ReadState, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	state, _, err := o.prepareLocked(ctx)
	if err != nil {
		return ReadState{}, inventoryUnavailable(err)
	}
	return state, nil
}

func (o *Owner) FindRecordedCommand(ctx context.Context, commandID string) (domain.CommandRecord, bool, error) {
	record, found, err := o.events.FindCommand(ctx, commandID)
	if err != nil {
		return domain.CommandRecord{}, false, err
	}
	return domain.CloneCommandRecord(record), found, nil
}

func (o *Owner) FindOrderByProposal(ctx context.Context, proposalID string) (domain.Order, bool, error) {
	order, found, err := o.events.FindOrderByProposal(ctx, proposalID)
	if err != nil {
		return domain.Order{}, false, err
	}
	return domain.CloneOrder(order), found, nil
}

func (o *Owner) prepareLocked(ctx context.Context) (ReadState, []contract.ExpiryResult, error) {
	if err := o.syncProjectionLocked(ctx); err != nil {
		return ReadState{}, nil, err
	}
	results := make([]contract.ExpiryResult, 0)
	for {
		now := domain.CanonicalBusinessTime(o.now())
		batchID, ok := o.nextDueBatch(now)
		if !ok {
			version, err := o.events.Version(ctx)
			if err != nil {
				o.usable = false
				return ReadState{}, results, err
			}
			if version != o.state.StateVersion {
				if err := o.syncProjectionLocked(ctx); err != nil {
					return ReadState{}, results, err
				}
				continue
			}
			return ReadState{State: domain.CloneInventoryState(o.state), EvaluatedAt: now, StateVersion: o.state.StateVersion}, results, nil
		}
		result, err := o.expireDueLocked(ctx, batchID, now)
		if err != nil {
			return ReadState{}, results, err
		}
		results = append(results, toContractExpiry(result))
		if !o.usable {
			if err := o.syncProjectionLocked(ctx); err != nil {
				return ReadState{}, results, err
			}
		}
	}
}
