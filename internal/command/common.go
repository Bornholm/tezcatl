package command

import (
	"github.com/bornholm/tezcatl/internal/config"
	"github.com/bornholm/tezcatl/internal/setup"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func commonFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "config",
			Usage: "path to the YAML configuration file",
		},
		&cli.StringFlag{
			Name:  "state-dir",
			Usage: "directory where learned state is persisted (overrides the configuration)",
		},
		&cli.BoolFlag{
			Name:  "debug-events",
			Usage: "emit one debug.observation event per observation",
		},
	}
}

func loadConfig(ctx *cli.Context) (*config.Config, error) {
	cfg, err := config.Load(ctx.String("config"))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if stateDir := ctx.String("state-dir"); stateDir != "" {
		cfg.State.Dir = stateDir
	}

	if ctx.Bool("debug-events") {
		cfg.Pipeline.DebugEvents = true
	}

	setup.SetupLogging(cfg)

	return cfg, nil
}
