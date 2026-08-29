package setup

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"

	"github.com/bornholm/tezcatl/internal/adapter/fs"
	"github.com/bornholm/tezcatl/internal/adapter/postgres"
	"github.com/bornholm/tezcatl/internal/adapter/stdio"
	"github.com/bornholm/tezcatl/internal/adapter/webhook"
	"github.com/bornholm/tezcatl/internal/config"
	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/bornholm/tezcatl/internal/core/correlate"
	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/drain"
	"github.com/bornholm/tezcatl/internal/core/engine"
	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/bornholm/tezcatl/internal/core/processor"
	"github.com/bornholm/tezcatl/internal/core/sink"
	"github.com/bornholm/tezcatl/internal/core/state"
	"github.com/bornholm/tezcatl/internal/plugin"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// Runtime is the fully composed pipeline for one tezcatl process,
// identical between the server and standalone modes except for the
// ingesters.
type Runtime struct {
	config       *config.Config
	ingesters    []port.Ingester
	processors   []port.Processor
	sinks        []port.EventSink
	snapshotters []port.Snapshotter
	stateStore   port.StateStore
	eventsOut    io.Writer

	miner          *drain.PartitionedMiner
	logDetector    *detect.LogDetector
	metricDetector *detect.MetricDetector
	broadcast      *sink.Broadcast
}

// AdminService exposes the runtime inspection and feedback operations
// of this pipeline (templates, markings, metric series, live events).
func (r *Runtime) AdminService() *admin.Service {
	return admin.NewService(r.miner, r.logDetector, r.metricDetector,
		admin.WithEventStream(r.broadcast))
}

type RuntimeOptionFunc func(r *Runtime)

// WithEventsOutput overrides the writer of the stdout sink (tests).
func WithEventsOutput(w io.Writer) RuntimeOptionFunc {
	return func(r *Runtime) {
		r.eventsOut = w
	}
}

func NewRuntime(ctx context.Context, cfg *config.Config, funcs ...RuntimeOptionFunc) (*Runtime, error) {
	runtime := &Runtime{
		config:    cfg,
		eventsOut: os.Stdout,
	}

	for _, fn := range funcs {
		fn(runtime)
	}

	if err := runtime.build(ctx); err != nil {
		runtime.Close()
		return nil, errors.WithStack(err)
	}

	return runtime, nil
}

func (r *Runtime) build(ctx context.Context) error {
	cfg := r.config

	// Processors: parse → normalize → template mining → analysis (→ debug).
	r.processors = []port.Processor{}

	if cfg.Logs.Parsing.Enabled == nil || *cfg.Logs.Parsing.Enabled {
		r.processors = append(r.processors, processor.NewParseLog())
	}

	r.processors = append(r.processors, processor.NewNormalize(processor.WithMaxLogLength(cfg.Pipeline.MaxLogLength)))

	r.miner = drain.NewPartitionedMiner(&cfg.Logs.Drain)
	mining := processor.NewTemplateMining(r.miner)
	r.processors = append(r.processors, mining)
	r.snapshotters = append(r.snapshotters, mining)

	detectors := []detect.Detector{}

	if cfg.Logs.Detection.Enabled == nil || *cfg.Logs.Detection.Enabled {
		r.logDetector = detect.NewLogDetector(cfg.LogDetectionConfig())
		detectors = append(detectors, r.logDetector)
		r.snapshotters = append(r.snapshotters, r.logDetector)
	}

	if cfg.Metrics.Detection.Enabled == nil || *cfg.Metrics.Detection.Enabled {
		r.metricDetector = detect.NewMetricDetector(cfg.MetricDetectionConfig())
		detectors = append(detectors, r.metricDetector)
		r.snapshotters = append(r.snapshotters, r.metricDetector)
	}

	if len(detectors) > 0 {
		correlator := correlate.NewCorrelator(cfg.CorrelationConfig())
		r.processors = append(r.processors, processor.NewAnalysis(correlator, detectors...))
	}

	if cfg.Pipeline.DebugEvents {
		r.processors = append(r.processors, processor.NewDebug())
	}

	// Sinks.
	if cfg.Sinks.Stdout.Enabled == nil || *cfg.Sinks.Stdout.Enabled {
		r.sinks = append(r.sinks, stdio.NewJSONLSink(r.eventsOut))
	}

	if cfg.Sinks.Postgres.Enabled {
		pg, err := postgres.NewEventSink(ctx, cfg.Sinks.Postgres.DSN)
		if err != nil {
			return errors.Wrap(err, "could not set up postgres sink")
		}

		r.sinks = append(r.sinks, sink.NewResilient("postgres", pg, cfg.Sinks.Postgres.QueueSize, cfg.Sinks.Postgres.MaxAttempts))
	}

	if cfg.Sinks.Webhook.Enabled {
		hook, err := webhook.NewEventSink(cfg.Sinks.Webhook.URL, cfg.Sinks.Webhook.Headers)
		if err != nil {
			return errors.Wrap(err, "could not set up webhook sink")
		}

		r.sinks = append(r.sinks, sink.NewResilient("webhook", hook, cfg.Sinks.Webhook.QueueSize, cfg.Sinks.Webhook.MaxAttempts))
	}

	if len(r.sinks) == 0 {
		return errors.New("no sink enabled")
	}

	// Last, and never counted as an enabled sink: the in-memory feed
	// backing "tezcatl top". It keeps a bounded history and drops
	// events rather than slowing the pipeline down.
	r.broadcast = sink.NewBroadcast(sink.DefaultHistorySize)
	r.sinks = append(r.sinks, r.broadcast)

	// Configuration-driven ingesters, added to the ones the command
	// provides. Active sources (Prometheus polling, host or Kubernetes
	// collection…) are plugins.
	for name, source := range cfg.Plugins.Sources {
		if !source.Enabled {
			continue
		}

		path, err := plugin.Lookup(plugin.Dir(cfg.Plugins.Dir), name)
		if err != nil {
			return errors.Wrapf(err, "could not set up source plugin %q", name)
		}

		pluginConfig, err := json.Marshal(source.Config)
		if err != nil {
			return errors.WithStack(err)
		}

		r.ingesters = append(r.ingesters, plugin.NewSourceIngester(name, path, pluginConfig))
	}

	// State persistence.
	if cfg.State.Dir != "" {
		store, err := fs.NewStateStore(cfg.State.Dir)
		if err != nil {
			return errors.Wrap(err, "could not set up state store")
		}

		r.stateStore = store
	}

	return nil
}

// Run executes the engine fed by the given ingesters, alongside the
// state persistence loop when enabled. It blocks until ingestion
// completes or the context is canceled.
func (r *Runtime) Run(ctx context.Context, ingesters ...port.Ingester) error {
	defer r.Close()

	ingesters = append(ingesters, r.ingesters...)

	opts := []engine.OptionFunc{
		engine.WithIngesters(ingesters...),
		engine.WithProcessors(r.processors...),
		engine.WithSinks(r.sinks...),
		engine.WithObservationBufferSize(r.config.Pipeline.ObservationBufferSize),
		engine.WithEventBufferSize(r.config.Pipeline.EventBufferSize),
		engine.WithFlushInterval(r.config.Pipeline.FlushInterval.AsDuration()),
		engine.WithStatsInterval(r.config.Logging.StatsInterval.AsDuration()),
	}

	if r.config.Pipeline.Workers > 0 {
		opts = append(opts, engine.WithWorkers(r.config.Pipeline.Workers))
	}

	if r.stateStore != nil {
		manager := state.NewManager(r.stateStore, r.config.State.SaveInterval.AsDuration(), r.snapshotters...)

		if err := manager.RestoreAll(ctx); err != nil {
			return errors.WithStack(err)
		}

		g, gctx := errgroup.WithContext(ctx)

		engineCtx, stopEngine := context.WithCancel(gctx)
		defer stopEngine()

		managerCtx, stopManager := context.WithCancel(context.WithoutCancel(gctx))

		g.Go(func() error {
			// The manager stops (and saves one final time) once the
			// engine is done.
			defer stopManager()

			if err := engine.New(opts...).Run(engineCtx); err != nil {
				return errors.WithStack(err)
			}

			return nil
		})

		g.Go(func() error {
			if err := manager.Run(managerCtx); err != nil {
				return errors.WithStack(err)
			}

			return nil
		})

		if err := g.Wait(); err != nil {
			return errors.WithStack(err)
		}

		return nil
	}

	if err := engine.New(opts...).Run(ctx); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// Close releases the sinks and the state store.
func (r *Runtime) Close() {
	for _, s := range r.sinks {
		if err := s.Close(); err != nil {
			slog.Error("could not close sink", slog.Any("error", err))
		}
	}
	r.sinks = nil

	if r.stateStore != nil {
		if err := r.stateStore.Close(); err != nil {
			slog.Error("could not close state store", slog.Any("error", err))
		}
		r.stateStore = nil
	}
}
