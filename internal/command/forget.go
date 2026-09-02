package command

import (
	"context"
	"fmt"
	"os"
	"path"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func NewForgetCommand() *cli.Command {
	return &cli.Command{
		Name:      "forget",
		Usage:     "Drop what was learned about one or more partitions (templates, baselines)",
		ArgsUsage: "<partition or glob, e.g. production/session-*>",
		Description: `Learning is normally worth keeping, and tezcatl never drops it on
its own. Some learning is worth nothing though: units that will never
come back under the same name, or lines ingested by mistake, leave
templates that no marking can remove and that will never match again.

Markings survive: silencing a template is a decision, not something
learned, and forgetting must not undo it.

The partition is the "<environment>/<service>" shown by
'tezcatl templates'. Run with --dry-run first: this cannot be undone
except by learning it all over again.`,
		Flags: append(adminTargetFlags(),
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "list what would be dropped, drop nothing",
			},
		),
		Action: func(ctx *cli.Context) error {
			pattern := ctx.Args().First()
			if pattern == "" {
				return errors.New("missing partition pattern (see 'tezcatl templates' for the names)")
			}

			if _, err := path.Match(pattern, ""); err != nil {
				return errors.Wrapf(err, "malformed pattern %q", pattern)
			}

			if ctx.Bool("dry-run") {
				return errors.WithStack(previewForget(ctx, pattern))
			}

			var (
				result admin.ForgetResult
				err    error
			)

			if stateDir := offlineStateDir(ctx); stateDir != "" {
				result, err = forgetOffline(ctx.Context, ctx.String("config"), stateDir, pattern)
			} else {
				result, err = forgetRemote(ctx.Context, ctx.String("target"), ctx.String("tls-ca"), pattern)
			}

			if err != nil {
				return errors.WithStack(err)
			}

			if len(result.Partitions) == 0 {
				fmt.Printf("no partition matches %q\n", pattern)

				return nil
			}

			for _, partition := range result.Partitions {
				fmt.Println(partition)
			}

			fmt.Printf("forgotten: %d partition(s), %d template(s), %d series\n",
				len(result.Partitions), result.Templates, result.Series)

			return nil
		},
	}
}

// previewForget shows what the pattern covers without touching
// anything, which is the only way to check a glob before it bites.
func previewForget(ctx *cli.Context, pattern string) error {
	var (
		templates []admin.TemplateInfo
		err       error
	)

	if stateDir := offlineStateDir(ctx); stateDir != "" {
		templates, err = listOffline(ctx.Context, ctx.String("config"), stateDir)
	} else {
		templates, err = listRemote(ctx.Context, ctx.String("target"), ctx.String("tls-ca"))
	}

	if err != nil {
		return errors.WithStack(err)
	}

	counts := map[string]int{}
	order := []string{}

	for _, template := range templates {
		if matched, _ := path.Match(pattern, template.Partition); !matched {
			continue
		}

		if _, seen := counts[template.Partition]; !seen {
			order = append(order, template.Partition)
		}

		counts[template.Partition]++
	}

	if len(order) == 0 {
		fmt.Printf("no partition matches %q\n", pattern)

		return nil
	}

	total := 0
	for _, partition := range order {
		fmt.Fprintf(os.Stdout, "%s\t%d template(s)\n", partition, counts[partition])
		total += counts[partition]
	}

	fmt.Printf("would forget %d partition(s) and %d template(s); metric baselines of those sources go too\n",
		len(order), total)

	return nil
}

func forgetRemote(ctx context.Context, target string, caFile string, pattern string) (admin.ForgetResult, error) {
	conn, err := grpc.Dial(target, caFile)
	if err != nil {
		return admin.ForgetResult{}, errors.WithStack(err)
	}
	defer conn.Close()

	res, err := tezcatlv1.NewAdminServiceClient(conn).Forget(ctx, &tezcatlv1.ForgetRequest{Pattern: pattern})
	if err != nil {
		return admin.ForgetResult{}, errors.WithStack(err)
	}

	return admin.ForgetResult{
		Partitions: res.GetPartitions(),
		Templates:  int(res.GetTemplates()),
		Series:     int(res.GetSeries()),
	}, nil
}

// forgetOffline edits a stopped server's state, then writes back every
// snapshot the operation touched: the mined templates, the log
// baselines and the metric ones.
func forgetOffline(ctx context.Context, configPath string, stateDir string, pattern string) (admin.ForgetResult, error) {
	runtime, err := openOffline(ctx, configPath, stateDir)
	if err != nil {
		return admin.ForgetResult{}, errors.WithStack(err)
	}
	defer runtime.store.Close()

	result, err := runtime.service.Forget(pattern)
	if err != nil {
		return result, errors.WithStack(err)
	}

	snapshots := []struct {
		key      string
		snapshot func() ([]byte, error)
	}{
		// "drain" is the key the server writes the mined templates
		// under; the two detectors name their own.
		{"drain", runtime.miner.Snapshot},
		{runtime.logDetector.SnapshotKey(), runtime.logDetector.Snapshot},
		{runtime.metricDetector.SnapshotKey(), runtime.metricDetector.Snapshot},
	}

	for _, entry := range snapshots {
		data, err := entry.snapshot()
		if err != nil {
			return result, errors.WithStack(err)
		}

		if err := runtime.store.Save(ctx, entry.key, data); err != nil {
			return result, errors.WithStack(err)
		}
	}

	return result, nil
}
