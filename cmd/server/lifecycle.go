package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"
)

type ticker interface {
	C() <-chan time.Time
	Stop()
}

type realTicker struct {
	ticker *time.Ticker
}

func (t realTicker) C() <-chan time.Time { return t.ticker.C }
func (t realTicker) Stop()               { t.ticker.Stop() }

type lifecycleDependencies struct {
	newTicker             func(time.Duration) ticker
	processDueExpirations func(context.Context) error
	runSnapshotWorker     func(context.Context) error
	serve                 func() error
	shutdown              func(context.Context) error
	logf                  func(string, ...any)
}

type lifecycleResult struct {
	done chan struct{}
	err  error
}

func runService(parent context.Context, cfg serverConfig, dependencies lifecycleDependencies) error {
	if cfg.expirationCheckInterval <= 0 {
		return errors.New("expiration check interval must be positive")
	}
	if dependencies.newTicker == nil || dependencies.processDueExpirations == nil || dependencies.runSnapshotWorker == nil || dependencies.serve == nil || dependencies.shutdown == nil {
		return errors.New("lifecycle dependencies are required")
	}
	if dependencies.logf == nil {
		dependencies.logf = log.Printf
	}
	expirationTicker := dependencies.newTicker(cfg.expirationCheckInterval)
	if expirationTicker == nil {
		return errors.New("ticker factory returned nil")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	expirationDone := &lifecycleResult{done: make(chan struct{})}
	go func() {
		defer close(expirationDone.done)
		defer expirationTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-expirationTicker.C():
				if !ok {
					expirationDone.err = errors.New("expiration ticker stopped unexpectedly")
					return
				}
				if err := dependencies.processDueExpirations(ctx); err != nil && ctx.Err() == nil {
					dependencies.logf("process due expirations: %v", err)
				}
			}
		}
	}()

	snapshotDone := &lifecycleResult{done: make(chan struct{})}
	go func() {
		defer close(snapshotDone.done)
		err := dependencies.runSnapshotWorker(ctx)
		if err == nil && ctx.Err() == nil {
			err = errors.New("snapshot worker stopped unexpectedly")
		}
		snapshotDone.err = err
	}()

	serveDone := &lifecycleResult{done: make(chan struct{})}
	go func() {
		defer close(serveDone.done)
		serveDone.err = dependencies.serve()
	}()

	shutdownDone := &lifecycleResult{done: make(chan struct{})}
	go func() {
		defer close(shutdownDone.done)
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		shutdownDone.err = dependencies.shutdown(shutdownCtx)
	}()

	var cause error
	var serveErr, snapshotErr, expirationErr error
	var serveFinished, snapshotFinished, expirationFinished bool
	var snapshotErrorReported bool
	select {
	case <-parent.Done():
	case <-serveDone.done:
		serveErr = serveDone.err
		serveFinished = true
	case <-snapshotDone.done:
		snapshotErr = snapshotDone.err
		snapshotFinished = true
		snapshotErrorShouldStop := snapshotErr != nil
		if parent.Err() != nil && errors.Is(snapshotErr, context.Canceled) {
			snapshotErrorShouldStop = false
		}
		if snapshotErrorShouldStop {
			cause = fmt.Errorf("run snapshot worker: %w", snapshotErr)
			snapshotErrorReported = true
		}
	case <-expirationDone.done:
		expirationErr = expirationDone.err
		expirationFinished = true
	}
	cancel()

	<-shutdownDone.done
	shutdownErr := shutdownDone.err
	if !serveFinished {
		<-serveDone.done
		serveErr = serveDone.err
	}
	if !snapshotFinished {
		<-snapshotDone.done
		snapshotErr = snapshotDone.err
	}
	if !expirationFinished {
		<-expirationDone.done
		expirationErr = expirationDone.err
	}
	if serveErr == nil {
		cause = errors.Join(cause, errors.New("HTTP server stopped unexpectedly"))
	} else if !errors.Is(serveErr, http.ErrServerClosed) {
		cause = errors.Join(cause, fmt.Errorf("serve HTTP: %w", serveErr))
	}
	snapshotFailed := snapshotErr != nil
	canceledNormally := ctx.Err() != nil && errors.Is(snapshotErr, context.Canceled)
	if snapshotFailed && !snapshotErrorReported && !canceledNormally {
		cause = errors.Join(cause, fmt.Errorf("run snapshot worker: %w", snapshotErr))
	}
	if expirationErr != nil {
		cause = errors.Join(cause, fmt.Errorf("expiration worker: %w", expirationErr))
	}
	if shutdownErr != nil {
		cause = errors.Join(cause, fmt.Errorf("shutdown HTTP server: %w", shutdownErr))
	}
	return cause
}
