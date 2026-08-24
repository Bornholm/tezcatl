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
				Flags: []cli.Flag{
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
				},
				Action: runStandaloneLogs,
			},
		},
	}
}

func runStandaloneLogs(ctx *cli.Context) error {
	processors := []port.Processor{}
	if ctx.Bool("debug-events") {
		processors = append(processors, processor.NewDebug())
	}

	opts := []engine.OptionFunc{
		engine.WithIngesters(stdio.NewLogIngester(os.Stdin, ctx.String("source"))),
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
