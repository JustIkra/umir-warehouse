package application

import "context"

// ctxMutex is a mutex whose waiters can leave. A caller whose context ends
// while it waits returns without having touched anything the mutex guards.
type ctxMutex struct {
	held chan struct{}
}

func newCtxMutex() ctxMutex { return ctxMutex{held: make(chan struct{}, 1)} }

// Lock waits for the mutex or for the end of ctx, whichever comes first. An
// already ended context never takes the mutex.
func (m *ctxMutex) Lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.held <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// lockUncancellable is for the few callers that have no context to give up with.
func (m *ctxMutex) lockUncancellable() { m.held <- struct{}{} }

// TryLock takes the mutex only if it is free.
func (m *ctxMutex) TryLock() bool {
	select {
	case m.held <- struct{}{}:
		return true
	default:
		return false
	}
}

func (m *ctxMutex) Unlock() { <-m.held }
