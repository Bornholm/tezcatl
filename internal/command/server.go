package command

import (
	"github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/setup"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func NewServerCommand() *cli.Command {
	return &cli.Command{
		Name:  "server",
		Usage: "Run the centralized ingestion server",
		Flags: append(commonFlags(),
			&cli.StringSliceFlag{
				Name:  "listen",
				Usage: "listen target, e.g. unix:///run/tezcatl.sock or tcp://127.0.0.1:4242 (repeatable, overrides the configuration)",
			},
		),
		Action: func(ctx *cli.Context) error {
			cfg, err := loadConfig(ctx)
			if err != nil {
				return errors.WithStack(err)
			}

			if listen := ctx.StringSlice("listen"); len(listen) > 0 {
				cfg.Server.Listen = listen
			}

			runtime, err := setup.NewRuntime(ctx.Context, cfg)
			if err != nil {
				return errors.WithStack(err)
			}

			adminServer := grpc.NewAdminServer(runtime.AdminService())

			serverOpts := []grpc.ServerIngesterOption{
				grpc.WithServices(adminServer.Register),
			}

			if cfg.Server.TLS.CertFile != "" || cfg.Server.TLS.KeyFile != "" {
				withTLS, err := grpc.WithTLS(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile)
				if err != nil {
					return errors.WithStack(err)
				}

				serverOpts = append(serverOpts, withTLS)
			}

			ingester := grpc.NewServerIngester(cfg.Server.Listen, serverOpts...)

			if err := runtime.Run(ctx.Context, ingester); err != nil {
				return errors.WithStack(err)
			}

			return nil
		},
	}
}
