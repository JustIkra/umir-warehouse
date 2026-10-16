package application

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

var ErrNotDue = contract.ErrNotDue

type Owner struct {
	loadZone          func(string) (*time.Location, error)
	tzdataVersion     string
	scope             contract.Scope
	mu                ctxMutex
	events            EventStore
	now               func() time.Time
	newID             func() string
	state             domain.InventoryState
	expirations       map[string]time.Time
	usable            bool
	pending           *domain.CommandBatch
	started           bool
	snapshotStore     SnapshotStore
	snapshotWorker    *SnapshotWorker
	snapshotEvery     int64
	lastSavedSequence int64
	logf              func(string, ...any)
}

// ReadState is the inventory a caller evaluates. Owner.Prepare returns an
// independent copy. prepareLocked returns a borrowed view of the Owner's own
// state, valid only while the caller holds the lock: do not keep or change it.
type ReadState struct {
	State        domain.InventoryState
	EvaluatedAt  time.Time
	StateVersion int64
}

func NewOwner(events EventStore, now func() time.Time, newID func() string) (*Owner, error) {
	if events == nil || now == nil || newID == nil {
		return nil, contract.ErrInvalidArgument
	}
	return &Owner{mu: newCtxMutex(), events: events, now: now, newID: newID, loadZone: domain.LoadZone, logf: log.Printf}, nil
}

// ConfigureLogger replaces the function the Owner and its snapshot worker log
// through; it must be called before the Owner starts.
func (o *Owner) ConfigureLogger(logf func(string, ...any)) error {
	o.mu.lockUncancellable()
	defer o.mu.Unlock()
	if o.started || logf == nil {
		return contract.ErrInvalidArgument
	}
	o.logf = logf
	if o.snapshotWorker != nil {
		o.snapshotWorker.setLogger(logf)
	}
	return nil
}

func (o *Owner) Recover(ctx context.Context) error {
	if err := o.mu.Lock(ctx); err != nil {
		return inventoryUnavailable(err)
	}
	defer o.mu.Unlock()
	o.started = true
	if err := o.recoverLocked(ctx); err != nil {
		return inventoryUnavailable(err)
	}
	if o.pending != nil {
		_, _, err := o.resolvePendingLocked(ctx)
		if err != nil {
			if errors.Is(err, domain.ErrConcurrentModification) {
				if recoverErr := o.recoverLocked(ctx); recoverErr != nil {
					return inventoryUnavailable(recoverErr)
				}
				return nil
			}
			return inventoryUnavailable(err)
		}
	}
	return nil
}

func (o *Owner) Prepare(ctx context.Context) (ReadState, error) {
	if err := o.mu.Lock(ctx); err != nil {
		return ReadState{}, inventoryUnavailable(err)
	}
	defer o.mu.Unlock()
	o.started = true
	state, _, err := o.prepareLocked(ctx)
	if err != nil {
		return ReadState{}, inventoryUnavailable(err)
	}
	state.State = domain.CloneReadView(state.State)
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

// prepareLocked returns a borrowed ReadState: State shares the Owner's maps and
// is valid only while o.mu is held. Callers must not keep it past the unlock,
// change it, or use it after a write that applies a batch.
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
				o.markUnusable(err)
				return ReadState{}, results, err
			}
			if version != o.state.StateVersion {
				if err := o.syncProjectionLocked(ctx); err != nil {
					return ReadState{}, results, err
				}
				continue
			}
			evaluatedAt := domain.CanonicalBusinessTime(o.now())
			if _, due := o.nextDueBatch(evaluatedAt); due {
				continue
			}
			return ReadState{State: o.state, EvaluatedAt: evaluatedAt, StateVersion: o.state.StateVersion}, results, nil
		}
		result, err := o.expireDueLocked(ctx, batchID, now)
		if err != nil {
			if errors.Is(err, domain.ErrConcurrentModification) {
				if syncErr := o.syncProjectionLocked(ctx); syncErr != nil {
					return ReadState{}, results, syncErr
				}
				continue
			}
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

func (o *Owner) ConfigureScope(scope contract.Scope) error {
	o.mu.lockUncancellable()
	defer o.mu.Unlock()
	if o.started || o.scope.RestaurantID != "" {
		return contract.ErrInvalidArgument
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	o.scope = scope
	return nil
}

// UpdateSettings replaces the time zone and settings version of the scope
// with newer ones for the same restaurant and lifecycle. The inventory stays.
func (o *Owner) UpdateSettings(ctx context.Context, scope contract.Scope) error {
	if err := o.mu.Lock(ctx); err != nil {
		return err
	}
	defer o.mu.Unlock()
	if scope.Validate() != nil || scope.OrganizationID != o.scope.OrganizationID || scope.RestaurantID != o.scope.RestaurantID || scope.RuntimeEpoch != o.scope.RuntimeEpoch || scope.LifecycleVersion != o.scope.LifecycleVersion {
		return contract.ErrInvalidArgument
	}
	o.scope.TimeZone, o.scope.SettingsVersion = scope.TimeZone, scope.SettingsVersion
	return nil
}

func (o *Owner) ConfigureTZData(version string) error {
	o.mu.lockUncancellable()
	defer o.mu.Unlock()
	if o.started {
		return contract.ErrInvalidArgument
	}
	o.tzdataVersion = version
	return nil
}

func (o *Owner) ConfigureZoneLoader(loader func(string) (*time.Location, error)) error {
	o.mu.lockUncancellable()
	defer o.mu.Unlock()
	if o.started || loader == nil {
		return contract.ErrInvalidArgument
	}
	o.loadZone = loader
	return nil
}

func (o *Owner) ValidateAnalyticsScope(scope contract.Scope) error {
	o.mu.lockUncancellable()
	defer o.mu.Unlock()
	if scope.Validate() != nil || scope.OrganizationID != o.scope.OrganizationID || scope.RestaurantID != o.scope.RestaurantID || scope.LifecycleVersion != o.scope.LifecycleVersion {
		return contract.ErrForbidden
	}
	if scope.RuntimeEpoch != o.scope.RuntimeEpoch {
		return contract.ErrRuntimeReset
	}
	return nil
}
