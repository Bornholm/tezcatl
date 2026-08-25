package command

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/adapter/stdio"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/bornholm/tezcatl/internal/plugin"
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
					return runIngest(ctx, stdio.NewLogIngester(os.Stdin, identity(ctx)))
				},
			},
			{
				Name:  "metrics",
				Usage: "Forward Prometheus text format metrics read from stdin",
				Flags: ingestFlags(),
				Action: func(ctx *cli.Context) error {
					return runIngest(ctx, stdio.NewMetricIngester(os.Stdin, identity(ctx)))
				},
			},
			{
				Name:      "source",
				Usage:     "Run an ingestion source plugin and forward its observations",
				ArgsUsage: "<plugin>",
				Flags: append(targetFlags(),
					&cli.StringFlag{
						Name:  "plugins-dir",
						Usage: "directory holding the plugin binaries (default: $TEZCATL_PLUGINS_DIR or /usr/lib/tezcatl/plugins)",
					},
					&cli.StringFlag{
						Name:  "source-config",
						Usage: "plugin configuration as JSON",
						Value: "{}",
					},
					&cli.StringFlag{
						Name:  "environment",
						Usage: "deployment environment, merged into the plugin configuration",
					},
				),
				Action: func(ctx *cli.Context) error {
					name := ctx.Args().First()
					if name == "" {
						return errors.New("missing plugin name, e.g.: tezcatl ingest source --target unix:///run/tezcatl/tezcatl.sock host")
					}

					path, err := plugin.Lookup(plugin.Dir(ctx.String("plugins-dir")), name)
					if err != nil {
						return errors.WithStack(err)
					}

					sourceConfig, err := mergeSourceConfig(ctx.String("source-config"), ctx.String("environment"))
					if err != nil {
						return errors.WithStack(err)
					}

					return runIngest(ctx, plugin.NewSourceIngester(name, path, sourceConfig))
				},
			},
			{
				Name:  "changes",
				Usage: "Forward change records (JSON Lines) read from stdin, e.g. from a CI pipeline or docker events",
				Flags: ingestFlags(),
				Action: func(ctx *cli.Context) error {
					return runIngest(ctx, stdio.NewChangeIngester(os.Stdin, identity(ctx)))
				},
			},
			{
				Name:  "change",
				Usage: "Report a single change (deployment, configuration, restart...)",
				Flags: append(ingestFlags(),
					&cli.StringFlag{
						Name:     "type",
						Usage:    "change type, e.g. deployment, config, restart",
						Required: true,
					},
					&cli.StringFlag{
						Name:  "change-version",
						Usage: "version identifier, e.g. checkout:v1.8.2",
					},
					&cli.StringFlag{
						Name:  "summary",
						Usage: "human readable description of the change",
					},
				),
				Action: func(ctx *cli.Context) error {
					now := time.Now()

					obs := model.Observation{
						ID:          model.NewID(),
						Service:     ctx.String("service"),
						Environment: ctx.String("environment"),
						Modality:    model.ModalityChange,
						Timestamp:   now,
						IngestedAt:  now,
						Change: &model.ChangeRecord{
							Type:    ctx.String("type"),
							Version: ctx.String("change-version"),
							Summary: ctx.String("summary"),
						},
					}

					return runIngest(ctx, singleObservation(obs))
				},
			},
		},
	}
}

type singleObservation model.Observation

func (o singleObservation) Ingest(ctx context.Context, out chan<- model.Observation) error {
	select {
	case out <- model.Observation(o):
		return nil
	case <-ctx.Done():
		return errors.WithStack(ctx.Err())
	}
}

func targetFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:     "target",
			Usage:    "server address, e.g. unix:///run/tezcatl.sock or tcp://host:4242",
			Required: true,
		},
		&cli.IntFlag{
			Name:  "buffer-size",
			Usage: "capacity of the local forwarding buffer",
			Value: 1024,
		},
		&cli.StringFlag{
			Name:  "tls-ca",
			Usage: "PEM CA bundle verifying a tls:// target (default: system roots)",
		},
	}
}

func ingestFlags() []cli.Flag {
	return append(identityFlags(), targetFlags()...)
}

func runIngest(ctx *cli.Context, ingesters ...port.Ingester) error {
	observations := make(chan model.Observation, ctx.Int("buffer-size"))

	client := grpc.NewClient(ctx.String("target"), grpc.ClientWithCA(ctx.String("tls-ca")))

	g, gctx := errgroup.WithContext(ctx.Context)

	ingest, ingestCtx := errgroup.WithContext(gctx)
	for _, ingester := range ingesters {
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

// mergeSourceConfig merges the --environment convenience flag into the
// plugin JSON configuration.
func mergeSourceConfig(raw string, environment string) ([]byte, error) {
	merged := map[string]any{}

	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &merged); err != nil {
			return nil, errors.Wrap(err, "malformed --source-config")
		}
	}

	if environment != "" {
		merged["environment"] = environment
	}

	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return encoded, nil
}
