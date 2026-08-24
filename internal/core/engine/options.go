package engine

import (
	"runtime"
	"time"

	"github.com/bornholm/tezcatl/internal/core/port"
)

type Options struct {
	Ingesters  []port.Ingester
	Processors []port.Processor
	Sinks      []port.EventSink

	Workers               int
	ObservationBufferSize int
	EventBufferSize       int
	FlushInterval         time.Duration
	// StatsInterval is how often internal health counters are logged;
	// 0 disables periodic logging (the final summary is always logged).
	StatsInterval time.Duration
}

type OptionFunc func(opts *Options)

func NewOptions(funcs ...OptionFunc) *Options {
	opts := &Options{
		Workers:               max(1, runtime.NumCPU()-1),
		ObservationBufferSize: 1024,
		EventBufferSize:       256,
		FlushInterval:         time.Second,
	}

	for _, fn := range funcs {
		fn(opts)
	}

	return opts
}

func WithIngesters(ingesters ...port.Ingester) OptionFunc {
	return func(opts *Options) {
		opts.Ingesters = ingesters
	}
}

func WithProcessors(processors ...port.Processor) OptionFunc {
	return func(opts *Options) {
		opts.Processors = processors
	}
}

func WithSinks(sinks ...port.EventSink) OptionFunc {
	return func(opts *Options) {
		opts.Sinks = sinks
	}
}

func WithWorkers(workers int) OptionFunc {
	return func(opts *Options) {
		opts.Workers = max(1, workers)
	}
}

func WithObservationBufferSize(size int) OptionFunc {
	return func(opts *Options) {
		opts.ObservationBufferSize = max(1, size)
	}
}

func WithEventBufferSize(size int) OptionFunc {
	return func(opts *Options) {
		opts.EventBufferSize = max(1, size)
	}
}

func WithFlushInterval(interval time.Duration) OptionFunc {
	return func(opts *Options) {
		if interval > 0 {
			opts.FlushInterval = interval
		}
	}
}

func WithStatsInterval(interval time.Duration) OptionFunc {
	return func(opts *Options) {
		opts.StatsInterval = interval
	}
}
