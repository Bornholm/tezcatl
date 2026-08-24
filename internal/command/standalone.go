package command

import (
	"os"

	"github.com/bornholm/tezcatl/internal/adapter/stdio"
	"github.com/bornholm/tezcatl/internal/core/engine"
	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/bornholm/tezcatl/internal/core/processor"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func NewStandaloneCommand() *cli.Command {
	return &cli.Command{
		Name:  "standalone",
		Usage: "Run the full pipeline locally, without a server",
		Subcommands: []*cli.Command{
			{
				Name:  "logs",
				Usage: "Process log lines read from stdin, emit events on stdout as JSON Lines",
				Flags: append(standaloneFlags(),
					&cli.StringFlag{
						Name:  "metrics-from",
						Usage: "also ingest Prometheus text format metrics from this file or FIFO",
					},
				),
				Action: func(ctx *cli.Context) error {
					ingesters := []port.Ingester{
						stdio.NewLogIngester(os.Stdin, ctx.String("source")),
					}

					if path := ctx.String("metrics-from"); path != "" {
						metrics, err := os.Open(path)
						if err != nil {
							return errors.WithStack(err)
						}
						defer metrics.Close()

						ingesters = append(ingesters, stdio.NewMetricIngester(metrics, ctx.String("source")))
					}

					return runStandalone(ctx, ingesters)
				},
			},
			{
				Name:  "metrics",
				Usage: "Process Prometheus text format metrics read from stdin, emit events on stdout as JSON Lines",
				Flags: standaloneFlags(),
				Action: func(ctx *cli.Context) error {
					return runStandalone(ctx, []port.Ingester{
						stdio.NewMetricIngester(os.Stdin, ctx.String("source")),
					})
				},
			},
		},
	}
}

func standaloneFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:     "source",
			Usage:    "logical source of the observations",
			Required: true,
		},
		&cli.IntFlag{
			Name:  "workers",
			Usage: "number of partitioned workers (0 = number of CPUs - 1)",
		},
		&cli.IntFlag{
			Name:  "observation-buffer-size",
			Usage: "capacity of the observation channels",
			Value: 1024,
		},
		&cli.IntFlag{
			Name:  "event-buffer-size",
			Usage: "capacity of the event channel",
			Value: 256,
		},
		&cli.BoolFlag{
			Name:  "debug-events",
			Usage: "emit one debug.observation event per observation (default until detectors exist)",
			Value: true,
		},
	}
}

func runStandalone(ctx *cli.Context, ingesters []port.Ingester) error {
	processors := []port.Processor{
		processor.NewNormalize(),
	}

	if ctx.Bool("debug-events") {
		processors = append(processors, processor.NewDebug())
	}

	opts := []engine.OptionFunc{
		engine.WithIngesters(ingesters...),
		engine.WithProcessors(processors...),
		engine.WithSinks(stdio.NewJSONLSink(os.Stdout)),
		engine.WithObservationBufferSize(ctx.Int("observation-buffer-size")),
		engine.WithEventBufferSize(ctx.Int("event-buffer-size")),
	}

	if workers := ctx.Int("workers"); workers > 0 {
		opts = append(opts, engine.WithWorkers(workers))
	}

	if err := engine.New(opts...).Run(ctx.Context); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
