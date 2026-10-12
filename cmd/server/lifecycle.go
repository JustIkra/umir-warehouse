package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"
)

type lifecycleDependencies struct {
	runSnapshotWorker func(context.Context) error
	waitSnapshotRetry func(context.Context, time.Duration) error
	serve             func() error
	shutdown          func(context.Context) error
	logf              func(string, ...any)
}

type lifecycleResult struct {
	done chan struct{}
	err  error
}

type retryableSnapshotSaveError interface {
	RetryableSnapshotSave()
}

func runService(parent context.Context, cfg serverConfig, dependencies lifecycleDependencies) error {
	if cfg.snapshotRetryInterval <= 0 {
		return errors.New("snapshot retry interval must be positive")
	}
	if dependencies.runSnapshotWorker == nil || dependencies.waitSnapshotRetry == nil || dependencies.serve == nil || dependencies.shutdown == nil {
		return errors.New("lifecycle dependencies are required")
	}
	if dependencies.logf == nil {
		dependencies.logf = log.Printf
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	snapshotDone := &lifecycleResult{done: make(chan struct{})}
	go func() {
		defer close(snapshotDone.done)
		snapshotDone.err = runSnapshotWorkerWithRetry(ctx, cfg.snapshotRetryInterval, dependencies.runSnapshotWorker, dependencies.waitSnapshotRetry, dependencies.logf)
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
	var serveErr, snapshotErr error
	var serveFinished, snapshotFinished bool
	var snapshotErrorReported bool
	select {
	case <-parent.Done():
	case <-serveDone.done:
		serveErr = serveDone.err
		serveFinished = true
	case <-snapshotDone.done:
		snapshotErr = snapshotDone.err
		snapshotFinished = true
		if snapshotErr != nil && (parent.Err() == nil || !errors.Is(snapshotErr, context.Canceled)) {
			cause = fmt.Errorf("run snapshot worker: %w", snapshotErr)
			snapshotErrorReported = true
		}
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
	if serveErr == nil {
		cause = errors.Join(cause, errors.New("HTTP server stopped unexpectedly"))
	} else if !errors.Is(serveErr, http.ErrServerClosed) {
		cause = errors.Join(cause, fmt.Errorf("serve HTTP: %w", serveErr))
	}
	canceledNormally := ctx.Err() != nil && errors.Is(snapshotErr, context.Canceled)
	if snapshotErr != nil && !snapshotErrorReported && !canceledNormally {
		cause = errors.Join(cause, fmt.Errorf("run snapshot worker: %w", snapshotErr))
	}
	if shutdownErr != nil {
		cause = errors.Join(cause, fmt.Errorf("shutdown HTTP server: %w", shutdownErr))
	}
	return cause
}

func runSnapshotWorkerWithRetry(ctx context.Context, retryInterval time.Duration, run func(context.Context) error, wait func(context.Context, time.Duration) error, logf func(string, ...any)) error {
	for {
		err := run(ctx)
		if ctx.Err() != nil {
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return ctx.Err()
		}
		if err == nil {
			return errors.New("snapshot worker stopped unexpectedly")
		}
		var saveErr retryableSnapshotSaveError
		if !errors.As(err, &saveErr) {
			return err
		}
		logf("snapshot save failed; retrying in %s: %v", retryInterval, err)
		if err := wait(ctx, retryInterval); err != nil {
			return err
		}
	}
}

func waitForSnapshotRetry(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
