package command

import (
	"github.com/bornholm/tezcatl/internal/build"
	"github.com/urfave/cli/v2"
)

func NewApp() *cli.App {
	return &cli.App{
		Name:    "tezcatl",
		Usage:   "intelligent multimodal observability platform",
		Version: build.LongVersion,
		Commands: []*cli.Command{
			NewServerCommand(),
			NewIngestCommand(),
			NewStandaloneCommand(),
			NewMarkCommand(),
			NewTemplatesCommand(),
			NewMetricsCommand(),
			NewEventsCommand(),
			NewIncidentsCommand(),
			NewTopCommand(),
			NewPluginCommand(),
		},
	}
}
