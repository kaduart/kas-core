package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	mrand "math/rand/v2"
	"sort"
	"sync"
	"time"
)

// ErrPermanent, wrapped in a handler error, sends the delivery straight to the
// dead state instead of retrying.
var ErrPermanent = errors.New("permanent handler failure")

// Handler processes one delivery. It must be idempotent: delivery is
// at-least-once, so the same event can reach a handler more than once (after a
// crash, a lost lease or a retry). Deduplicate by Event.EventID with
// Event.TenantID and Event.ServiceID.
type Handler interface {
	Handle(ctx context.Context, d Delivery) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, d Delivery) error

func (f HandlerFunc) Handle(ctx context.Context, d Delivery) error { return f(ctx, d) }

// DeliveryStore is what a Worker needs from storage.
type DeliveryStore interface {
	Claim(ctx context.Context, workerID string, consumers []string, now time.Time, lease time.Duration, maxAttempts, limit int) ([]Delivery, error)
	Complete(ctx context.Context, workerID string, d Delivery, now time.Time) (bool, error)
	Retry(ctx context.Context, workerID string, d Delivery, next time.Time, code string) (bool, error)
	Bury(ctx context.Context, workerID string, d Delivery, now time.Time, code string) (bool, error)
}

// WorkerConfig holds tunables; zero values select the documented defaults.
type WorkerConfig struct {
	ID             string        // default: random
	LeaseDuration  time.Duration // default 60s; must exceed HandlerTimeout
	HandlerTimeout time.Duration // default 30s
	PollInterval   time.Duration // default 1s
	BatchSize      int           // default 10, at most 100; handlers of a batch run concurrently
	MaxAttempts    int           // default 5
	BaseBackoff    time.Duration // default 2s
	MaxBackoff     time.Duration // default 5m
	Now            func() time.Time
	Jitter         func() float64 // in [0,1); default random
	Logger         *slog.Logger
}

// Worker claims deliveries for the consumers it has handlers for, runs the
// handlers and records the outcome. Several workers may run concurrently.
type Worker struct {
	store     DeliveryStore
	handlers  map[string]Handler
	consumers []string
	cfg       WorkerConfig
}

func NewWorker(store DeliveryStore, handlers map[string]Handler, cfg WorkerConfig) (*Worker, error) {
	if store == nil || len(handlers) == 0 {
		return nil, errors.New("a worker needs a store and at least one handler")
	}
	if cfg.LeaseDuration == 0 {
		cfg.LeaseDuration = 60 * time.Second
	}
	if cfg.HandlerTimeout == 0 {
		cfg.HandlerTimeout = 30 * time.Second
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 10
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.BaseBackoff == 0 {
		cfg.BaseBackoff = 2 * time.Second
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 5 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Jitter == nil {
		cfg.Jitter = defaultJitter
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ID == "" {
		var b [8]byte
		_, _ = rand.Read(b[:])
		cfg.ID = "worker-" + hex.EncodeToString(b[:])
	}
	switch {
	case cfg.HandlerTimeout >= cfg.LeaseDuration:
		return nil, errors.New("handler timeout must be shorter than the lease duration")
	case cfg.BatchSize < 1 || cfg.BatchSize > 100:
		return nil, errors.New("batch size must be between 1 and 100")
	case cfg.MaxAttempts < 1:
		return nil, errors.New("max attempts must be at least 1")
	case cfg.BaseBackoff > cfg.MaxBackoff:
		return nil, errors.New("base backoff must not exceed max backoff")
	}
	consumers := make([]string, 0, len(handlers))
	for id, handler := range handlers {
		if handler == nil {
			return nil, fmt.Errorf("nil handler for consumer %q", id)
		}
		consumers = append(consumers, id)
	}
	sort.Strings(consumers)
	return &Worker{store: store, handlers: handlers, consumers: consumers, cfg: cfg}, nil
}

// ID returns the worker's lease owner identifier.
func (w *Worker) ID() string { return w.cfg.ID }

// Backoff returns the delay before the next attempt after the given attempt
// number (1-based): base doubling per attempt, jittered by ±20%, capped at max.
func Backoff(attempt int, base, max time.Duration, jitter float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := float64(base) * math.Pow(2, float64(attempt-1))
	if delay > float64(max) {
		delay = float64(max)
	}
	out := time.Duration(delay * (0.8 + 0.4*jitter))
	if out > max {
		out = max
	}
	return out
}

// RunOnce claims one batch and processes it, returning how many deliveries it
// claimed. Handlers of the batch run concurrently, each bounded by HandlerTimeout.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	deliveries, err := w.store.Claim(ctx, w.cfg.ID, w.consumers, w.cfg.Now(), w.cfg.LeaseDuration, w.cfg.MaxAttempts, w.cfg.BatchSize)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, d := range deliveries {
		wg.Add(1)
		go func(d Delivery) {
			defer wg.Done()
			w.process(ctx, d)
		}(d)
	}
	wg.Wait()
	return len(deliveries), nil
}

// Run polls until ctx is cancelled, finishing in-flight handlers before it
// returns. It returns nil on cancellation.
func (w *Worker) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		n, err := w.RunOnce(ctx)
		if err != nil {
			w.cfg.Logger.Warn("outbox_claim_failed", "worker", w.cfg.ID)
		}
		if n == 0 || err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(w.cfg.PollInterval):
			}
		}
	}
	return nil
}

func (w *Worker) process(ctx context.Context, d Delivery) {
	code, err := w.invoke(ctx, d)
	// Settle even if ctx was cancelled during shutdown, within a bounded time.
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	now := w.cfg.Now()
	var applied bool
	var storeErr error
	outcome := "delivered"
	switch {
	case err == nil:
		applied, storeErr = w.store.Complete(settleCtx, w.cfg.ID, d, now)
	case errors.Is(err, ErrPermanent):
		outcome = "dead"
		applied, storeErr = w.store.Bury(settleCtx, w.cfg.ID, d, now, "handler_permanent")
	case d.Attempt >= w.cfg.MaxAttempts:
		outcome = "dead"
		applied, storeErr = w.store.Bury(settleCtx, w.cfg.ID, d, now, "max_attempts")
	default:
		outcome = "retry"
		next := now.Add(Backoff(d.Attempt, w.cfg.BaseBackoff, w.cfg.MaxBackoff, w.cfg.Jitter()))
		applied, storeErr = w.store.Retry(settleCtx, w.cfg.ID, d, next, code)
	}
	switch {
	case storeErr != nil:
		// The lease will expire and the delivery will be claimed again.
		w.cfg.Logger.Error("outbox_settle_failed", "worker", w.cfg.ID, "delivery", d.ID, "outcome", outcome)
	case !applied:
		w.cfg.Logger.Warn("outbox_lease_lost", "worker", w.cfg.ID, "delivery", d.ID, "attempt", d.Attempt)
	default:
		w.cfg.Logger.Info("outbox_delivery_settled", "worker", w.cfg.ID, "delivery", d.ID, "attempt", d.Attempt, "outcome", outcome)
	}
}

// invoke runs the handler with a timeout and panic recovery. It returns a short
// error code (never error text or payload) for the delivery record.
func (w *Worker) invoke(ctx context.Context, d Delivery) (code string, err error) {
	handlerCtx, cancel := context.WithTimeout(ctx, w.cfg.HandlerTimeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			code, err = "handler_panic", errors.New("handler panicked")
		}
	}()
	err = w.handlers[d.ConsumerID].Handle(handlerCtx, d)
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, ErrPermanent):
		return "handler_permanent", err
	case ctx.Err() != nil:
		return "handler_cancelled", err
	case errors.Is(handlerCtx.Err(), context.DeadlineExceeded):
		return "handler_timeout", err
	default:
		return "handler_error", err
	}
}

func defaultJitter() float64 { return mrand.Float64() }
