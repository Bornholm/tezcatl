package command

import (
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func NewIngestCommand() *cli.Command {
	return &cli.Command{
		Name:  "ingest",
		Usage: "Forward observations to a remote tezcatl server",
		Subcommands: []*cli.Command{
			{
				Name:  "logs",
				Usage: "Forward log lines read from stdin",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "target",
						Usage: "server address, e.g. unix:///run/tezcatl.sock or tcp://host:4242",
					},
					&cli.StringFlag{
						Name:  "source",
						Usage: "logical source of the observations",
					},
				},
				Action: func(ctx *cli.Context) error {
					return errors.New("not implemented yet, see phase 2 in docs/ROADMAP.md")
				},
			},
		},
	}
}
