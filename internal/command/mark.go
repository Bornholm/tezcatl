package command

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/adapter/fs"
	"github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/config"
	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/drain"
	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func adminTargetFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "target",
			Usage: "running server to talk to, e.g. unix:///run/tezcatl.sock",
		},
		&cli.StringFlag{
			Name:  "state-dir",
			Usage: "persisted state directory to edit offline (stopped process)",
		},
		&cli.StringFlag{
			Name:  "config",
			Usage: "path to the YAML configuration file (offline mode)",
		},
	}
}

func NewMarkCommand() *cli.Command {
	return &cli.Command{
		Name:  "mark",
		Usage: "Mark a log template as normal, ignore or symptomatic",
		Flags: append(adminTargetFlags(),
			&cli.StringFlag{
				Name:     "template",
				Usage:    "exact template string, as shown by 'tezcatl templates'",
				Required: true,
			},
			&cli.StringFlag{
				Name:  "as",
				Usage: "marking: normal, ignore or symptomatic",
			},
			&cli.BoolFlag{
				Name:  "clear",
				Usage: "clear the marking instead of setting one",
			},
		),
		Action: func(ctx *cli.Context) error {
			marking := detect.Marking(ctx.String("as"))

			if ctx.Bool("clear") {
				marking = ""
			} else if marking == "" {
				return errors.New("either --as or --clear is required")
			}

			if target := ctx.String("target"); target != "" {
				return markRemote(ctx.Context, target, ctx.String("template"), marking)
			}

			if stateDir := ctx.String("state-dir"); stateDir != "" {
				return markOffline(ctx.Context, ctx.String("config"), stateDir, ctx.String("template"), marking)
			}

			return errors.New("either --target or --state-dir is required")
		},
	}
}

func NewTemplatesCommand() *cli.Command {
	return &cli.Command{
		Name:  "templates",
		Usage: "List the learned log templates with sizes and markings",
		Flags: adminTargetFlags(),
		Action: func(ctx *cli.Context) error {
			var (
				templates []admin.TemplateInfo
				err       error
			)

			if target := ctx.String("target"); target != "" {
				templates, err = listRemote(ctx.Context, target)
			} else if stateDir := ctx.String("state-dir"); stateDir != "" {
				templates, err = listOffline(ctx.Context, ctx.String("config"), stateDir)
			} else {
				return errors.New("either --target or --state-dir is required")
			}

			if err != nil {
				return errors.WithStack(err)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "PARTITION\tID\tSIZE\tMARKING\tTEMPLATE")

			for _, template := range templates {
				fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\n", template.Partition, template.ID, template.Size, template.Marking, template.Template)
			}

			return errors.WithStack(w.Flush())
		},
	}
}

func markRemote(ctx context.Context, target string, template string, marking detect.Marking) error {
	conn, err := grpc.Dial(target)
	if err != nil {
		return errors.WithStack(err)
	}
	defer conn.Close()

	client := tezcatlv1.NewAdminServiceClient(conn)

	if _, err := client.MarkTemplate(ctx, &tezcatlv1.MarkTemplateRequest{
		Template: template,
		Marking:  string(marking),
	}); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func listRemote(ctx context.Context, target string) ([]admin.TemplateInfo, error) {
	conn, err := grpc.Dial(target)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer conn.Close()

	client := tezcatlv1.NewAdminServiceClient(conn)

	res, err := client.ListTemplates(ctx, &tezcatlv1.ListTemplatesRequest{})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	templates := make([]admin.TemplateInfo, 0, len(res.GetTemplates()))
	for _, template := range res.GetTemplates() {
		templates = append(templates, admin.TemplateInfo{
			Partition: template.GetPartition(),
			ID:        template.GetId(),
			Template:  template.GetTemplate(),
			Size:      template.GetSize(),
			Marking:   detect.Marking(template.GetMarking()),
		})
	}

	return templates, nil
}

// offlineService rebuilds the miner and detector from a persisted state
// directory, without a running process.
func offlineService(ctx context.Context, configPath string, stateDir string) (*admin.Service, *detect.LogDetector, port.StateStore, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}

	store, err := fs.NewStateStore(stateDir)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}

	miner := drain.NewPartitionedMiner(&cfg.Logs.Drain)
	if data, err := store.Load(ctx, "drain"); err == nil {
		if err := miner.Restore(data); err != nil {
			return nil, nil, nil, errors.WithStack(err)
		}
	} else if !errors.Is(err, port.ErrStateNotFound) {
		return nil, nil, nil, errors.WithStack(err)
	}

	detector := detect.NewLogDetector(cfg.LogDetectionConfig())
	if data, err := store.Load(ctx, detector.SnapshotKey()); err == nil {
		if err := detector.Restore(data); err != nil {
			return nil, nil, nil, errors.WithStack(err)
		}
	} else if !errors.Is(err, port.ErrStateNotFound) {
		return nil, nil, nil, errors.WithStack(err)
	}

	return admin.NewService(miner, detector), detector, store, nil
}

func markOffline(ctx context.Context, configPath string, stateDir string, template string, marking detect.Marking) error {
	service, detector, store, err := offlineService(ctx, configPath, stateDir)
	if err != nil {
		return errors.WithStack(err)
	}
	defer store.Close()

	if err := service.MarkTemplate(template, marking); err != nil {
		return errors.WithStack(err)
	}

	snapshot, err := detector.Snapshot()
	if err != nil {
		return errors.WithStack(err)
	}

	if err := store.Save(ctx, detector.SnapshotKey(), snapshot); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func listOffline(ctx context.Context, configPath string, stateDir string) ([]admin.TemplateInfo, error) {
	service, _, store, err := offlineService(ctx, configPath, stateDir)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer store.Close()

	return service.Templates(), nil
}
