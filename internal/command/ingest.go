package command

import (
	"os"

	"github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/adapter/stdio"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"golang.org/x/sync/errgroup"
)

func NewIngestCommand() *cli.Command {
	return &cli.Command{
		Name:  "ingest",
		Usage: "Forward observations to a remote tezcatl server",
		Subcommands: []*cli.Command{
			{
				Name:  "logs",
				Usage: "Forward log lines read from stdin",
				Flags: ingestFlags(),
				Action: func(ctx *cli.Context) error {
					return runIngest(ctx, stdio.NewLogIngester(os.Stdin, ctx.String("source")))
				},
			},
			{
				Name:  "metrics",
				Usage: "Forward Prometheus text format metrics read from stdin",
				Flags: ingestFlags(),
				Action: func(ctx *cli.Context) error {
					return runIngest(ctx, stdio.NewMetricIngester(os.Stdin, ctx.String("source")))
				},
			},
		},
	}
}

func ingestFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:     "target",
			Usage:    "server address, e.g. unix:///run/tezcatl.sock or tcp://host:4242",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "source",
			Usage:    "logical source of the observations",
			Required: true,
		},
		&cli.IntFlag{
			Name:  "buffer-size",
			Usage: "capacity of the local forwarding buffer",
			Value: 1024,
		},
	}
}

func runIngest(ctx *cli.Context, ingester port.Ingester) error {
	observations := make(chan model.Observation, ctx.Int("buffer-size"))

	client := grpc.NewClient(ctx.String("target"))

	g, gctx := errgroup.WithContext(ctx.Context)

	g.Go(func() error {
		defer close(observations)

		if err := ingester.Ingest(gctx, observations); err != nil {
			return errors.WithStack(err)
		}

		return nil
	})

	g.Go(func() error {
		if err := client.Forward(gctx, observations); err != nil {
			return errors.WithStack(err)
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
