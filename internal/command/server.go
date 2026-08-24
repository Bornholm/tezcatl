package command

import (
	"os"

	"github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/adapter/stdio"
	"github.com/bornholm/tezcatl/internal/core/engine"
	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/bornholm/tezcatl/internal/core/processor"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func NewServerCommand() *cli.Command {
	return &cli.Command{
		Name:  "server",
		Usage: "Run the centralized ingestion server",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "path to the YAML configuration file",
			},
			&cli.StringSliceFlag{
				Name:  "listen",
				Usage: "listen target, e.g. unix:///run/tezcatl.sock or tcp://127.0.0.1:4242 (repeatable)",
				Value: cli.NewStringSlice("tcp://127.0.0.1:4242"),
			},
			&cli.IntFlag{
				Name:  "workers",
				Usage: "number of partitioned workers (0 = number of CPUs - 1)",
			},
			&cli.BoolFlag{
				Name:  "debug-events",
				Usage: "emit one debug.observation event per observation (default until detectors exist)",
				Value: true,
			},
		},
		Action: func(ctx *cli.Context) error {
			processors := []port.Processor{
				processor.NewNormalize(),
			}

			if ctx.Bool("debug-events") {
				processors = append(processors, processor.NewDebug())
			}

			opts := []engine.OptionFunc{
				engine.WithIngesters(grpc.NewServerIngester(ctx.StringSlice("listen")...)),
				engine.WithProcessors(processors...),
				engine.WithSinks(stdio.NewJSONLSink(os.Stdout)),
			}

			if workers := ctx.Int("workers"); workers > 0 {
				opts = append(opts, engine.WithWorkers(workers))
			}

			if err := engine.New(opts...).Run(ctx.Context); err != nil {
				return errors.WithStack(err)
			}

			return nil
		},
	}
}
