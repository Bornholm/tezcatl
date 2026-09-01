// Itztli is the web face of a tezcatl server: incidents, templates
// and metrics behind a single-account or OIDC login. It is a separate
// binary because it is optional — the server runs the same without it.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bornholm/tezcatl/internal/build"
	"github.com/bornholm/tezcatl/internal/core/incident"
	itzclient "github.com/bornholm/tezcatl/internal/itztli/client"
	itzconfig "github.com/bornholm/tezcatl/internal/itztli/config"
	"github.com/bornholm/tezcatl/internal/itztli/explain"
	"github.com/bornholm/tezcatl/internal/itztli/server"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	app := &cli.App{
		Name:    "itztli",
		Usage:   "Web UI for a running tezcatl server: incidents, templates, metrics",
		Version: build.LongVersion,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Usage:   "path to the YAML configuration file",
				EnvVars: []string{"ITZTLI_CONFIG"},
			},
		},
		Action: run,
	}

	if err := app.RunContext(ctx, os.Args); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("fatal error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx *cli.Context) error {
	cfg, err := itzconfig.Load(ctx.String("config"))
	if err != nil {
		return errors.WithStack(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	admin, err := itzclient.New(itzclient.Options{
		Target: cfg.Tezcatl.Target,
		TLSCA:  cfg.Tezcatl.TLSCA,
		Window: cfg.Incidents.Window.AsDuration(),
		Grouping: incident.Options{
			Gap:          cfg.Incidents.Gap.AsDuration(),
			MaxDuration:  cfg.Incidents.MaxDuration.AsDuration(),
			CoOccurrence: cfg.Incidents.CoOccurrence.AsDuration(),
		},
		CacheTTL: cfg.Incidents.CacheTTL.AsDuration(),
	})
	if err != nil {
		return errors.WithStack(err)
	}

	defer admin.Close()

	explainer, err := explain.New(ctx.Context, cfg.GenAI)
	if err != nil {
		return errors.WithStack(err)
	}

	if explainer == nil {
		logger.Info("no genai provider configured, the Explain feature is disabled")
	}

	return errors.WithStack(
		server.New(cfg, admin, explainer, build.ShortVersion, logger).ListenAndServe(ctx.Context))
}
