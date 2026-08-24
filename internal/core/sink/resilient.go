package sink

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/pkg/errors"
)

const (
	DefaultQueueSize   = 1024
	DefaultMaxAttempts = 5

	retryInitialBackoff = time.Second
	retryMaxBackoff     = 30 * time.Second
	publishTimeout      = 10 * time.Second
)

// Resilient decorates an EventSink with a bounded queue and background
// retries, so a temporarily failing destination (e.g. PostgreSQL being
// down) never blocks the pipeline. When the queue is full, new events are
// dropped and counted; when an event exhausts its attempts, it is dropped
// with an error log.
type Resilient struct {
	name  string
	inner port.EventSink

	queue          chan model.Event
	maxAttempts    int
	initialBackoff time.Duration
	dropped        atomic.Int64

	wg        sync.WaitGroup
	closeOnce sync.Once
}

func NewResilient(name string, inner port.EventSink, queueSize int, maxAttempts int) *Resilient {
	return newResilient(name, inner, queueSize, maxAttempts, retryInitialBackoff)
}

func newResilient(name string, inner port.EventSink, queueSize int, maxAttempts int, initialBackoff time.Duration) *Resilient {
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}

	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}

	r := &Resilient{
		name:           name,
		inner:          inner,
		queue:          make(chan model.Event, queueSize),
		maxAttempts:    maxAttempts,
		initialBackoff: initialBackoff,
	}

	r.wg.Go(r.run)

	return r
}

func (r *Resilient) Publish(ctx context.Context, events []model.Event) error {
	for _, evt := range events {
		select {
		case r.queue <- evt:
		default:
			if dropped := r.dropped.Add(1); dropped == 1 || dropped%100 == 0 {
				slog.WarnContext(ctx, "sink queue full, dropping events", slog.String("sink", r.name), slog.Int64("dropped_total", dropped))
			}
		}
	}

	return nil
}

// Close drains the queue, then closes the underlying sink.
func (r *Resilient) Close() error {
	r.closeOnce.Do(func() {
		close(r.queue)
	})

	r.wg.Wait()

	if err := r.inner.Close(); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func (r *Resilient) run() {
	for evt := range r.queue {
		r.publishWithRetries(evt)
	}
}

func (r *Resilient) publishWithRetries(evt model.Event) {
	backoff := r.initialBackoff

	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
		err := r.inner.Publish(ctx, []model.Event{evt})
		cancel()

		if err == nil {
			return
		}

		if attempt >= r.maxAttempts {
			slog.Error("dropping event after failed publishes", slog.String("sink", r.name), slog.String("event", evt.ID), slog.Int("attempts", attempt), slog.Any("error", err))
			return
		}

		slog.Warn("publish failed, retrying", slog.String("sink", r.name), slog.String("event", evt.ID), slog.Int("attempt", attempt), slog.Duration("backoff", backoff), slog.Any("error", err))

		time.Sleep(backoff)
		backoff = min(backoff*2, retryMaxBackoff)
	}
}

// Dropped returns the number of events dropped because the queue was
// full.
func (r *Resilient) Dropped() int64 {
	return r.dropped.Load()
}
