package engine

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// Engine runs the observation pipeline:
//
//	ingesters → dispatch (partitioned) → workers (processor chain) → sinks
//
// All channels are bounded; shutdown cascades by closing channels once
// every ingester has returned, so buffered observations are drained
// before Run returns.
type Engine struct {
	opts  *Options
	stats stats
}

// stats are the internal health counters of the pipeline.
type stats struct {
	ingested  atomic.Int64
	processed atomic.Int64
	rejected  atomic.Int64
	events    atomic.Int64
}

func (s *stats) log(ctx context.Context, message string) {
	slog.InfoContext(ctx, message,
		slog.Int64("observations_ingested", s.ingested.Load()),
		slog.Int64("observations_processed", s.processed.Load()),
		slog.Int64("observations_rejected", s.rejected.Load()),
		slog.Int64("events_published", s.events.Load()),
	)
}

func New(funcs ...OptionFunc) *Engine {
	return &Engine{opts: NewOptions(funcs...)}
}

func (e *Engine) Run(ctx context.Context) error {
	if len(e.opts.Ingesters) == 0 {
		return errors.New("no ingester configured")
	}

	observations := make(chan model.Observation, e.opts.ObservationBufferSize)
	events := make(chan model.Event, e.opts.EventBufferSize)

	workerInputs := make([]chan model.Observation, e.opts.Workers)
	for i := range workerInputs {
		workerInputs[i] = make(chan model.Observation, e.opts.ObservationBufferSize)
	}

	g, gctx := errgroup.WithContext(ctx)

	ingest, ingestCtx := errgroup.WithContext(gctx)
	for _, ingester := range e.opts.Ingesters {
		ingest.Go(func() error {
			if err := ingester.Ingest(ingestCtx, observations); err != nil {
				return errors.WithStack(err)
			}

			return nil
		})
	}

	g.Go(func() error {
		defer close(observations)

		if err := ingest.Wait(); err != nil {
			return errors.WithStack(err)
		}

		return nil
	})

	g.Go(func() error {
		defer func() {
			for _, input := range workerInputs {
				close(input)
			}
		}()

		for obs := range observations {
			e.stats.ingested.Add(1)

			input := workerInputs[partitionIndex(obs.PartitionKey(), len(workerInputs))]

			select {
			case input <- obs:
			case <-gctx.Done():
				return errors.WithStack(gctx.Err())
			}
		}

		return nil
	})

	statsDone := make(chan struct{})

	if e.opts.StatsInterval > 0 {
		g.Go(func() error {
			ticker := time.NewTicker(e.opts.StatsInterval)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					e.stats.log(gctx, "pipeline stats")
				case <-statsDone:
					return nil
				case <-gctx.Done():
					return nil
				}
			}
		})
	}

	workers, workersCtx := errgroup.WithContext(gctx)
	for _, input := range workerInputs {
		workers.Go(func() error {
			return e.runWorker(workersCtx, input, events)
		})
	}

	g.Go(func() error {
		defer close(events)

		emit := func(evt model.Event) {
			select {
			case events <- evt:
			case <-gctx.Done():
			}
		}

		flusherDone := make(chan struct{})
		var flusherWait sync.WaitGroup

		flusherWait.Go(func() {
			e.runFlushers(gctx, flusherDone, emit)
		})

		err := workers.Wait()

		close(flusherDone)
		flusherWait.Wait()

		// Final flush: the pipeline is draining, emit whatever is
		// pending regardless of correlation windows.
		for _, proc := range e.opts.Processors {
			if flusher, ok := proc.(port.Flusher); ok {
				flusher.Flush(gctx, true, emit)
			}
		}

		if err != nil {
			return errors.WithStack(err)
		}

		return nil
	})

	g.Go(func() error {
		defer close(statsDone)

		for evt := range events {
			e.stats.events.Add(1)

			for _, sink := range e.opts.Sinks {
				if err := sink.Publish(gctx, []model.Event{evt}); err != nil {
					// A failing sink must not take the pipeline down.
					slog.ErrorContext(gctx, "could not publish event", slog.String("event", evt.ID), slog.Any("error", errors.WithStack(err)))
				}
			}
		}

		return nil
	})

	err := g.Wait()

	e.stats.log(ctx, "pipeline stopped")

	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// runFlushers periodically flushes the processors holding time-based
// state, so pending events are emitted even when no observation arrives.
func (e *Engine) runFlushers(ctx context.Context, done <-chan struct{}, emit func(evt model.Event)) {
	flushers := []port.Flusher{}
	for _, proc := range e.opts.Processors {
		if flusher, ok := proc.(port.Flusher); ok {
			flushers = append(flushers, flusher)
		}
	}

	if len(flushers) == 0 {
		return
	}

	ticker := time.NewTicker(e.opts.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			for _, flusher := range flushers {
				flusher.Flush(ctx, false, emit)
			}
		case <-done:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (e *Engine) runWorker(ctx context.Context, input <-chan model.Observation, events chan<- model.Event) error {
	emit := func(evt model.Event) {
		select {
		case events <- evt:
		case <-ctx.Done():
		}
	}

	for obs := range input {
		rejected := false

		for _, proc := range e.opts.Processors {
			next, err := proc.Process(ctx, &obs, emit)
			if err != nil {
				// Drop the observation, keep the pipeline alive.
				slog.ErrorContext(ctx, "processor failed", slog.String("processor", proc.Name()), slog.String("observation", obs.ID), slog.Any("error", errors.WithStack(err)))
				rejected = true
				break
			}

			if !next {
				rejected = true
				break
			}
		}

		if rejected {
			e.stats.rejected.Add(1)
		} else {
			e.stats.processed.Add(1)
		}
	}

	return nil
}

func partitionIndex(key string, total int) int {
	h := fnv.New32a()
	h.Write([]byte(key))

	return int(h.Sum32() % uint32(total))
}
