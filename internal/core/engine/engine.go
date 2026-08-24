package engine

import (
	"context"
	"hash/fnv"
	"log/slog"

	"github.com/bornholm/tezcatl/internal/core/model"
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
	opts *Options
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
			input := workerInputs[partitionIndex(obs.PartitionKey(), len(workerInputs))]

			select {
			case input <- obs:
			case <-gctx.Done():
				return errors.WithStack(gctx.Err())
			}
		}

		return nil
	})

	workers, workersCtx := errgroup.WithContext(gctx)
	for _, input := range workerInputs {
		workers.Go(func() error {
			return e.runWorker(workersCtx, input, events)
		})
	}

	g.Go(func() error {
		defer close(events)

		if err := workers.Wait(); err != nil {
			return errors.WithStack(err)
		}

		return nil
	})

	g.Go(func() error {
		for evt := range events {
			for _, sink := range e.opts.Sinks {
				if err := sink.Publish(gctx, []model.Event{evt}); err != nil {
					// A failing sink must not take the pipeline down.
					slog.ErrorContext(gctx, "could not publish event", slog.String("event", evt.ID), slog.Any("error", errors.WithStack(err)))
				}
			}
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func (e *Engine) runWorker(ctx context.Context, input <-chan model.Observation, events chan<- model.Event) error {
	emit := func(evt model.Event) {
		select {
		case events <- evt:
		case <-ctx.Done():
		}
	}

	for obs := range input {
		for _, proc := range e.opts.Processors {
			next, err := proc.Process(ctx, &obs, emit)
			if err != nil {
				// Drop the observation, keep the pipeline alive.
				slog.ErrorContext(ctx, "processor failed", slog.String("processor", proc.Name()), slog.String("observation", obs.ID), slog.Any("error", errors.WithStack(err)))
				break
			}

			if !next {
				break
			}
		}
	}

	return nil
}

func partitionIndex(key string, total int) int {
	h := fnv.New32a()
	h.Write([]byte(key))

	return int(h.Sum32() % uint32(total))
}
