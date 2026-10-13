package bootstrap

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/httpapi"
	"github.com/JustIkra/umir-warehouse/internal/adapters/zonedata"
)

type registeredRuntime struct {
	scope             contract.Scope
	runtime           *Runtime
	coverageFrom      time.Time
	closed            bool
	closeOperation    string
	registerOperation string
}
type Registry struct {
	mu         sync.RWMutex
	options    Options
	epoch      string
	tenants    map[string]registeredRuntime
	registered chan struct{}
}

func NewRegistry(options Options, epoch string) (*Registry, error) {
	if err := zonedata.Check(); err != nil {
		return nil, err
	}
	if options.TZDataVersion != "" && options.TZDataVersion != zonedata.Version {
		return nil, contract.ErrTimeZoneDataUnavailable
	}
	options.TZDataVersion = zonedata.Version
	if options.Now == nil || options.NewID == nil || options.EventsPerSnapshot <= 0 || !contract.ValidEntityID(epoch) {
		return nil, contract.ErrInvalidArgument
	}
	return &Registry{options: options, epoch: epoch, tenants: make(map[string]registeredRuntime), registered: make(chan struct{}, 1)}, nil
}
func (r *Registry) Register(ctx context.Context, scope contract.Scope) error {
	_, err := r.register(ctx, scope, "")
	return err
}
func (r *Registry) RegisterRuntime(ctx context.Context, scope contract.Scope, operationID string) (contract.RuntimeReadiness, error) {
	if !contract.ValidEntityID(operationID) {
		return contract.RuntimeReadiness{}, contract.ErrInvalidArgument
	}
	return r.register(ctx, scope, operationID)
}
func (r *Registry) register(ctx context.Context, scope contract.Scope, operationID string) (contract.RuntimeReadiness, error) {
	if err := ctx.Err(); err != nil {
		return contract.RuntimeReadiness{}, err
	}
	if err := scope.Validate(); err != nil {
		return contract.RuntimeReadiness{}, err
	}
	if _, err := zonedata.Load(scope.TimeZone); err != nil {
		return contract.RuntimeReadiness{}, contract.ErrInvalidArgument
	}
	if scope.RuntimeEpoch != r.epoch {
		return contract.RuntimeReadiness{}, contract.ErrRuntimeReset
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contract.RuntimeReadiness{}, err
	}
	if old, ok := r.tenants[scope.RestaurantID]; ok {
		if old.scope.OrganizationID != scope.OrganizationID {
			return contract.RuntimeReadiness{}, contract.ErrForbidden
		}
		if old.closed {
			return contract.RuntimeReadiness{}, contract.ErrScopeDeleted
		}
		if old.scope.LifecycleVersion != scope.LifecycleVersion {
			return contract.RuntimeReadiness{}, contract.ErrForbidden
		}
		if old.scope.TimeZone != scope.TimeZone || old.scope.SettingsVersion != scope.SettingsVersion {
			return contract.RuntimeReadiness{}, contract.ErrCommandIdentityConflict
		}
		if operationID != "" {
			if old.registerOperation != "" && old.registerOperation != operationID {
				return contract.RuntimeReadiness{}, contract.ErrCommandIdentityConflict
			}
			old.registerOperation = operationID
			r.tenants[scope.RestaurantID] = old
		}
		return r.readiness(old), nil
	}
	coverage := r.options.Now()
	if coverage.IsZero() {
		return contract.RuntimeReadiness{}, contract.ErrRuntimeNotReady
	}
	options := r.options
	options.Scope = scope
	options.CoverageFrom = coverage
	runtime, err := NewInMemory(options)
	if err != nil {
		return contract.RuntimeReadiness{}, err
	}
	if err := runtime.Recover(ctx); err != nil {
		runtime.fence()
		return contract.RuntimeReadiness{}, err
	}
	entry := registeredRuntime{scope: scope, runtime: runtime, coverageFrom: coverage, registerOperation: operationID}
	r.tenants[scope.RestaurantID] = entry
	select {
	case r.registered <- struct{}{}:
	default:
	}
	return r.readiness(entry), nil
}
func (r *Registry) readiness(entry registeredRuntime) contract.RuntimeReadiness {
	state := "ready"
	if entry.closed {
		state = "pending"
		if entry.runtime == nil {
			state = "closed"
		}
	}
	return contract.RuntimeReadiness{RestaurantID: entry.scope.RestaurantID, Service: "warehouse", Epoch: r.epoch, State: state, LifecycleVersion: entry.scope.LifecycleVersion}
}
func (r *Registry) Resolve(ctx context.Context, scope contract.Scope) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := scope.ValidateLifecycle(); err != nil {
		return nil, contract.ErrForbidden
	}
	if scope.RuntimeEpoch != r.epoch {
		return nil, contract.ErrRuntimeReset
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tenant, ok := r.tenants[scope.RestaurantID]
	if !ok {
		return nil, contract.ErrRuntimeNotReady
	}
	if tenant.scope.OrganizationID != scope.OrganizationID {
		return nil, contract.ErrForbidden
	}
	if tenant.closed {
		return nil, contract.ErrScopeDeleted
	}
	if tenant.scope.LifecycleVersion != scope.LifecycleVersion {
		return nil, contract.ErrForbidden
	}
	return tenant.runtime, nil
}
func (r *Registry) CloseRuntime(ctx context.Context, scope contract.Scope, operationID string) (contract.RuntimeReadiness, error) {
	if err := ctx.Err(); err != nil {
		return contract.RuntimeReadiness{}, err
	}
	if scope.ValidateLifecycle() != nil || !contract.ValidEntityID(operationID) {
		return contract.RuntimeReadiness{}, contract.ErrInvalidArgument
	}
	if scope.RuntimeEpoch != r.epoch {
		return contract.RuntimeReadiness{}, contract.ErrRuntimeReset
	}
	r.mu.Lock()
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return contract.RuntimeReadiness{}, err
	}
	entry, exists := r.tenants[scope.RestaurantID]
	if exists {
		if entry.scope.OrganizationID != scope.OrganizationID {
			r.mu.Unlock()
			return contract.RuntimeReadiness{}, contract.ErrForbidden
		}
		oldVersion, oldErr := strconv.ParseInt(entry.scope.LifecycleVersion, 10, 64)
		newVersion, newErr := strconv.ParseInt(scope.LifecycleVersion, 10, 64)
		if oldErr != nil || newErr != nil {
			r.mu.Unlock()
			return contract.RuntimeReadiness{}, contract.ErrInvalidArgument
		}
		if newVersion < oldVersion || !entry.closed && newVersion == oldVersion {
			r.mu.Unlock()
			return contract.RuntimeReadiness{}, contract.ErrForbidden
		}
		if entry.closed && (entry.closeOperation != operationID || entry.scope.LifecycleVersion != scope.LifecycleVersion) {
			r.mu.Unlock()
			return contract.RuntimeReadiness{}, contract.ErrCommandIdentityConflict
		}
	}
	if !entry.closed {
		entry.closed = true
		entry.scope = scope
		entry.closeOperation = operationID
		if entry.runtime != nil {
			entry.runtime.fence()
		}
		r.tenants[scope.RestaurantID] = entry
	}
	pending := r.readiness(entry)
	r.mu.Unlock()
	if entry.runtime != nil {
		if err := entry.runtime.cleanup(ctx); err != nil {
			return pending, err
		}
		r.mu.Lock()
		entry.runtime = nil
		entry.coverageFrom = time.Time{}
		r.tenants[scope.RestaurantID] = entry
		r.mu.Unlock()
	}
	return r.readiness(entry), nil
}
func (r *Registry) Coverage(ctx context.Context, scope contract.Scope) (time.Time, error) {
	if _, err := r.Resolve(ctx, scope); err != nil {
		return time.Time{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry := r.tenants[scope.RestaurantID]
	if entry.closed {
		return time.Time{}, contract.ErrScopeDeleted
	}
	return entry.coverageFrom, nil
}
func (r *Registry) Epoch() string { return r.epoch }
func (r *Registry) RuntimeFor(ctx context.Context, scope contract.Scope) (httpapi.Runtime, error) {
	return r.Resolve(ctx, scope)
}

func (r *Registry) RunSnapshotWorker(parent context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var workers sync.WaitGroup
	defer workers.Wait()
	type workerResult struct {
		runtime *Runtime
		err     error
	}
	results := make(chan workerResult, 1)
	started := make(map[*Runtime]bool)
	start := func() {
		r.mu.RLock()
		defer r.mu.RUnlock()
		for _, entry := range r.tenants {
			runtime := entry.runtime
			if entry.closed {
				continue
			}
			if started[runtime] {
				continue
			}
			started[runtime] = true
			workers.Go(func() {
				err := runtime.RunSnapshotWorker(ctx)
				if err == nil {
					err = errors.New("restaurant snapshot worker stopped")
				}
				select {
				case results <- workerResult{runtime, err}:
				case <-ctx.Done():
				}
			})
		}
	}
	start()
	for {
		select {
		case <-parent.Done():
			cancel()
			return parent.Err()
		case result := <-results:
			delete(started, result.runtime)
			if errors.Is(result.err, contract.ErrScopeDeleted) {
				continue
			}
			cancel()
			return result.err
		case <-r.registered:
			start()
		}
	}
}
