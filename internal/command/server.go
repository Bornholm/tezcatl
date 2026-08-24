package command

import (
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
		},
		Action: func(ctx *cli.Context) error {
			return errors.New("not implemented yet, see phase 2 in docs/ROADMAP.md")
		},
	}
}
