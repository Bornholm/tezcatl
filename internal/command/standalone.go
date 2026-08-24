package command

import (
	"os"

	"github.com/bornholm/tezcatl/internal/adapter/stdio"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/bornholm/tezcatl/internal/setup"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func identityFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:     "service",
			Usage:    "canonical name of the observed service",
			Required: true,
		},
		&cli.StringFlag{
			Name:  "environment",
			Usage: "deployment environment of the observed service",
			Value: model.DefaultEnvironment,
		},
	}
}

func identity(ctx *cli.Context) stdio.Identity {
	return stdio.Identity{
		Service:     ctx.String("service"),
		Environment: ctx.String("environment"),
	}
}

func NewStandaloneCommand() *cli.Command {
	return &cli.Command{
		Name:  "standalone",
		Usage: "Run the full pipeline locally, without a server",
		Subcommands: []*cli.Command{
			{
				Name:  "logs",
				Usage: "Process log lines read from stdin, emit events on stdout as JSON Lines",
				Flags: append(append(commonFlags(), identityFlags()...),
					&cli.StringFlag{
						Name:  "metrics-from",
						Usage: "also ingest Prometheus text format metrics from this file or FIFO",
					},
					&cli.StringFlag{
						Name:  "changes-from",
						Usage: "also ingest change records (JSON Lines) from this file or FIFO",
					},
				),
				Action: func(ctx *cli.Context) error {
					ingesters := []port.Ingester{
						stdio.NewLogIngester(os.Stdin, identity(ctx)),
					}

					if path := ctx.String("metrics-from"); path != "" {
						metrics, err := os.Open(path)
						if err != nil {
							return errors.WithStack(err)
						}
						defer metrics.Close()

						ingesters = append(ingesters, stdio.NewMetricIngester(metrics, identity(ctx)))
					}

					if path := ctx.String("changes-from"); path != "" {
						changes, err := os.Open(path)
						if err != nil {
							return errors.WithStack(err)
						}
						defer changes.Close()

						ingesters = append(ingesters, stdio.NewChangeIngester(changes, identity(ctx)))
					}

					return runStandalone(ctx, ingesters)
				},
			},
			{
				Name:  "metrics",
				Usage: "Process Prometheus text format metrics read from stdin, emit events on stdout as JSON Lines",
				Flags: append(commonFlags(), identityFlags()...),
				Action: func(ctx *cli.Context) error {
					return runStandalone(ctx, []port.Ingester{
						stdio.NewMetricIngester(os.Stdin, identity(ctx)),
					})
				},
			},
		},
	}
}

func runStandalone(ctx *cli.Context, ingesters []port.Ingester) error {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return errors.WithStack(err)
	}

	runtime, err := setup.NewRuntime(ctx.Context, cfg)
	if err != nil {
		return errors.WithStack(err)
	}

	if err := runtime.Run(ctx.Context, ingesters...); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
