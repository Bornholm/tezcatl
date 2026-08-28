package command

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"text/tabwriter"

	"github.com/bornholm/tezcatl/internal/plugin"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func pluginsDirFlag() *cli.StringFlag {
	return &cli.StringFlag{
		Name:  "plugins-dir",
		Usage: "directory holding the plugin binaries (default: $TEZCATL_PLUGINS_DIR or /usr/lib/tezcatl/plugins)",
	}
}

func NewPluginCommand() *cli.Command {
	return &cli.Command{
		Name:  "plugin",
		Usage: "Manage ingestion source plugins",
		Subcommands: []*cli.Command{
			{
				Name:      "install",
				Usage:     "Install a source plugin from a GitHub repository releasing goreleaser archives",
				ArgsUsage: "<github.com/owner/repo> [plugin]",
				Flags: []cli.Flag{
					pluginsDirFlag(),
					&cli.StringFlag{
						Name:  "version",
						Usage: "release tag to install (default: latest release)",
					},
				},
				Action: func(ctx *cli.Context) error {
					repo := ctx.Args().First()
					if repo == "" {
						return errors.New("missing repository, e.g.: tezcatl plugin install github.com/bornholm/tezcatl prometheus")
					}

					name, err := plugin.Install(ctx.Context, plugin.InstallOptions{
						Repo:    repo,
						Name:    ctx.Args().Get(1),
						Version: ctx.String("version"),
						Dir:     plugin.Dir(ctx.String("plugins-dir")),
						OS:      runtime.GOOS,
						Arch:    runtime.GOARCH,
					})
					if err != nil {
						return errors.WithStack(err)
					}

					fmt.Printf("plugin %q installed in %s\n", name, plugin.Dir(ctx.String("plugins-dir")))

					return nil
				},
			},
			{
				Name:  "list",
				Usage: "List the installed source plugins",
				Flags: []cli.Flag{pluginsDirFlag()},
				Action: func(ctx *cli.Context) error {
					plugins, err := plugin.Discover(plugin.Dir(ctx.String("plugins-dir")))
					if err != nil {
						return errors.WithStack(err)
					}

					names := make([]string, 0, len(plugins))
					for name := range plugins {
						names = append(names, name)
					}
					sort.Strings(names)

					w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
					fmt.Fprintln(w, "NAME\tPATH")

					for _, name := range names {
						fmt.Fprintf(w, "%s\t%s\n", name, plugins[name])
					}

					return errors.WithStack(w.Flush())
				},
			},
			{
				Name:      "remove",
				Usage:     "Remove an installed source plugin",
				ArgsUsage: "<name>",
				Flags:     []cli.Flag{pluginsDirFlag()},
				Action: func(ctx *cli.Context) error {
					name := ctx.Args().First()
					if name == "" {
						return errors.New("missing plugin name")
					}

					dir := plugin.Dir(ctx.String("plugins-dir"))

					path, err := plugin.Lookup(dir, name)
					if err != nil {
						return errors.WithStack(err)
					}

					if err := os.Remove(path); err != nil {
						return errors.WithStack(err)
					}

					fmt.Printf("plugin %q removed from %s\n", name, filepath.Dir(path))

					return nil
				},
			},
		},
	}
}
