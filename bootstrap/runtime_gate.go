package bootstrap

import (
	"context"
	"sync"

	"github.com/JustIkra/umir-warehouse/contract"
)

type runtimeGate struct {
	mu      sync.Mutex
	closed  bool
	active  int
	drained chan struct{}
	stop    context.Context
	cancel  context.CancelFunc
}

func newRuntimeGate() *runtimeGate {
	ctx, cancel := context.WithCancel(context.Background())
	return &runtimeGate{drained: make(chan struct{}), stop: ctx, cancel: cancel}
}
func (r *Runtime) enter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.gate == nil {
		return nil
	}
	r.gate.mu.Lock()
	defer r.gate.mu.Unlock()
	if r.gate.closed {
		return contract.ErrScopeDeleted
	}
	r.gate.active++
	return nil
}
func (r *Runtime) leave() {
	if r.gate == nil {
		return
	}
	r.gate.mu.Lock()
	defer r.gate.mu.Unlock()
	r.gate.active--
	if r.gate.closed && r.gate.active == 0 {
		close(r.gate.drained)
	}
}
func (r *Runtime) deleted() bool {
	if r.gate == nil {
		return false
	}
	r.gate.mu.Lock()
	defer r.gate.mu.Unlock()
	return r.gate.closed
}
func (r *Runtime) fence() {
	r.gate.mu.Lock()
	defer r.gate.mu.Unlock()
	if r.gate.closed {
		return
	}
	r.gate.closed = true
	r.gate.cancel()
	if r.productBatches != nil {
		r.productBatches.Close()
	}
	if r.gate.active == 0 {
		close(r.gate.drained)
	}
}
func (r *Runtime) cleanup(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.gate.drained:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.gate.mu.Lock()
	defer r.gate.mu.Unlock()
	r.owner = nil
	r.attempts = nil
	r.productBatches = nil
	return nil
}
