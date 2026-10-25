package service

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

// Dispatcher runs background jobs (e.g. webhook-triggered reviews) with
// bounded concurrency, a bounded queue, and a graceful shutdown that waits
// for in-flight work.
type Dispatcher struct {
	ctx        context.Context
	cancel     context.CancelFunc
	sem        chan struct{}
	maxPending int
	jobTimeout time.Duration
	logger     *slog.Logger

	mu      sync.Mutex
	pending int
	closed  bool
	wg      sync.WaitGroup
}

// NewDispatcher creates a dispatcher. Jobs run detached from any HTTP request
// context so that they survive after the webhook has been acknowledged.
func NewDispatcher(concurrency, maxPending int, jobTimeout time.Duration, logger *slog.Logger) *Dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		ctx:        ctx,
		cancel:     cancel,
		sem:        make(chan struct{}, concurrency),
		maxPending: maxPending,
		jobTimeout: jobTimeout,
		logger:     logger,
	}
}

// Submit enqueues fn. It returns domain.ErrShuttingDown or domain.ErrQueueFull
// when the job cannot be accepted.
func (d *Dispatcher) Submit(name string, fn func(ctx context.Context)) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return domain.ErrShuttingDown
	}
	if d.maxPending > 0 && d.pending >= d.maxPending {
		d.mu.Unlock()
		return domain.ErrQueueFull
	}
	d.pending++
	d.wg.Add(1)
	d.mu.Unlock()

	go func() {
		defer d.wg.Done()
		defer func() {
			d.mu.Lock()
			d.pending--
			d.mu.Unlock()
		}()

		select {
		case d.sem <- struct{}{}:
		case <-d.ctx.Done():
			d.logger.Warn("job dropped during shutdown", slog.String("job", name))
			return
		}
		defer func() { <-d.sem }()

		ctx, cancel := context.WithTimeout(d.ctx, d.jobTimeout)
		defer cancel()
		defer func() {
			if rec := recover(); rec != nil {
				d.logger.Error("job panicked",
					slog.String("job", name),
					slog.String("panic", fmt.Sprint(rec)),
					slog.String("stack", string(debug.Stack())),
				)
			}
		}()
		fn(ctx)
	}()
	return nil
}

// Shutdown stops accepting jobs and waits for in-flight jobs to finish. If ctx
// expires first, running jobs are cancelled and Shutdown returns ctx.Err().
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		d.cancel()
		return nil
	case <-ctx.Done():
		d.cancel()
		// Give cancelled jobs a brief moment to unwind and log.
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return ctx.Err()
	}
}
