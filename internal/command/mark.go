package command

import (
	"context"
	"fmt"
	"os"
	"strconv"
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

// defaultTarget is the socket served by the packaged tezcatl-server
// unit (RuntimeDirectory=tezcatl).
const defaultTarget = "unix:///run/tezcatl/tezcatl.sock"

func adminTargetFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "target",
			Usage:   "running server to talk to, e.g. tcp://host:4242",
			Value:   defaultTarget,
			EnvVars: []string{"TEZCATL_TARGET"},
		},
		&cli.StringFlag{
			Name:  "state-dir",
			Usage: "persisted state directory to edit offline (stopped process)",
		},
		&cli.StringFlag{
			Name:  "config",
			Usage: "path to the YAML configuration file (offline mode)",
		},
		&cli.StringFlag{
			Name:  "tls-ca",
			Usage: "PEM CA bundle verifying a tls:// target (default: system roots)",
		},
	}
}

func NewMarkCommand() *cli.Command {
	return &cli.Command{
		Name:  "mark",
		Usage: "Mark a log template (normal, ignore, symptomatic) or silence a metric series (ignore)",
		Flags: append(adminTargetFlags(),
			&cli.StringFlag{
				Name:  "template",
				Usage: "exact template string, as shown by 'tezcatl templates'",
			},
			&cli.StringFlag{
				Name:  "metric",
				Usage: "series key as shown by 'tezcatl metrics', a metric name, or a glob over either",
			},
			&cli.StringFlag{
				Name:  "as",
				Usage: "marking: normal, ignore or symptomatic (metrics: ignore only)",
			},
			&cli.BoolFlag{
				Name:  "clear",
				Usage: "clear the marking instead of setting one",
			},
		),
		Action: func(ctx *cli.Context) error {
			template, metric := ctx.String("template"), ctx.String("metric")
			if (template == "") == (metric == "") {
				return errors.New("exactly one of --template or --metric is required")
			}

			marking := detect.Marking(ctx.String("as"))

			if ctx.Bool("clear") {
				marking = ""
			} else if marking == "" {
				return errors.New("either --as or --clear is required")
			}

			if metric != "" && marking != "" && marking != detect.MarkingIgnore {
				return errors.Errorf("a metric can only be marked ignore, not %q", marking)
			}

			if stateDir := offlineStateDir(ctx); stateDir != "" {
				if metric != "" {
					return markMetricOffline(ctx.Context, ctx.String("config"), stateDir, metric, marking == detect.MarkingIgnore)
				}

				return markOffline(ctx.Context, ctx.String("config"), stateDir, template, marking)
			}

			if metric != "" {
				return markMetricRemote(ctx.Context, ctx.String("target"), ctx.String("tls-ca"), metric, marking == detect.MarkingIgnore)
			}

			return markRemote(ctx.Context, ctx.String("target"), ctx.String("tls-ca"), template, marking)
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

			if stateDir := offlineStateDir(ctx); stateDir != "" {
				templates, err = listOffline(ctx.Context, ctx.String("config"), stateDir)
			} else {
				templates, err = listRemote(ctx.Context, ctx.String("target"), ctx.String("tls-ca"))
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

func NewMetricsCommand() *cli.Command {
	return &cli.Command{
		Name:  "metrics",
		Usage: "List the learned metric series with their baselines",
		Flags: adminTargetFlags(),
		Action: func(ctx *cli.Context) error {
			var (
				series []detect.SeriesInfo
				err    error
			)

			if stateDir := offlineStateDir(ctx); stateDir != "" {
				series, err = listMetricsOffline(ctx.Context, ctx.String("config"), stateDir)
			} else {
				series, err = listMetricsRemote(ctx.Context, ctx.String("target"), ctx.String("tls-ca"))
			}

			if err != nil {
				return errors.WithStack(err)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SERIES\tSAMPLES\tMEAN\tSTDDEV\tRECENT\tSTATE")

			for _, info := range series {
				state := "ready"
				if info.Warmup {
					state = "warming up"
				}
				if info.Ignored {
					state = "ignored"
				}

				fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n",
					info.Key, info.Samples,
					formatValue(info.Mean), formatValue(info.StdDev), formatValue(info.Recent),
					state)
			}

			return errors.WithStack(w.Flush())
		},
	}
}

// offlineStateDir returns the state directory to edit offline, or ""
// to talk to a server: an explicit --target wins over --state-dir,
// otherwise the packaged socket is the default.
func offlineStateDir(ctx *cli.Context) string {
	if ctx.IsSet("target") {
		return ""
	}

	return ctx.String("state-dir")
}

func formatValue(value float64) string {
	return strconv.FormatFloat(value, 'g', 4, 64)
}

func listMetricsRemote(ctx context.Context, target string, caFile string) ([]detect.SeriesInfo, error) {
	conn, err := grpc.Dial(target, caFile)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer conn.Close()

	client := tezcatlv1.NewAdminServiceClient(conn)

	res, err := client.ListMetrics(ctx, &tezcatlv1.ListMetricsRequest{})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return seriesFromProto(res.GetMetrics()), nil
}

func seriesFromProto(metrics []*tezcatlv1.MetricInfo) []detect.SeriesInfo {
	series := make([]detect.SeriesInfo, 0, len(metrics))
	for _, info := range metrics {
		series = append(series, detect.SeriesInfo{
			Key:     info.GetKey(),
			Samples: info.GetSamples(),
			Mean:    info.GetMean(),
			StdDev:  info.GetStdDev(),
			Recent:  info.GetRecent(),
			Warmup:  info.GetWarmup(),
			Ignored: info.GetIgnored(),
		})
	}

	return series
}

func listMetricsOffline(ctx context.Context, configPath string, stateDir string) ([]detect.SeriesInfo, error) {
	service, _, _, store, err := offlineService(ctx, configPath, stateDir)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer store.Close()

	return service.Metrics(), nil
}

func markMetricRemote(ctx context.Context, target string, caFile string, pattern string, ignore bool) error {
	conn, err := grpc.Dial(target, caFile)
	if err != nil {
		return errors.WithStack(err)
	}
	defer conn.Close()

	client := tezcatlv1.NewAdminServiceClient(conn)

	if _, err := client.MarkMetric(ctx, &tezcatlv1.MarkMetricRequest{
		Pattern: pattern,
		Ignore:  ignore,
	}); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func markMetricOffline(ctx context.Context, configPath string, stateDir string, pattern string, ignore bool) error {
	service, _, metricDetector, store, err := offlineService(ctx, configPath, stateDir)
	if err != nil {
		return errors.WithStack(err)
	}
	defer store.Close()

	if err := service.MarkMetric(pattern, ignore); err != nil {
		return errors.WithStack(err)
	}

	snapshot, err := metricDetector.Snapshot()
	if err != nil {
		return errors.WithStack(err)
	}

	if err := store.Save(ctx, metricDetector.SnapshotKey(), snapshot); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func markRemote(ctx context.Context, target string, caFile string, template string, marking detect.Marking) error {
	conn, err := grpc.Dial(target, caFile)
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

func listRemote(ctx context.Context, target string, caFile string) ([]admin.TemplateInfo, error) {
	conn, err := grpc.Dial(target, caFile)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer conn.Close()

	client := tezcatlv1.NewAdminServiceClient(conn)

	res, err := client.ListTemplates(ctx, &tezcatlv1.ListTemplatesRequest{})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return templatesFromProto(res.GetTemplates()), nil
}

func templatesFromProto(infos []*tezcatlv1.TemplateInfo) []admin.TemplateInfo {
	templates := make([]admin.TemplateInfo, 0, len(infos))
	for _, template := range infos {
		templates = append(templates, admin.TemplateInfo{
			Partition: template.GetPartition(),
			ID:        template.GetId(),
			Template:  template.GetTemplate(),
			Size:      template.GetSize(),
			Marking:   detect.Marking(template.GetMarking()),
		})
	}

	return templates
}

// offlineService rebuilds the miner and detector from a persisted state
// directory, without a running process.
// offlineRuntime is a stopped server's state, opened for reading or
// editing: the same objects the running server holds, restored from
// disk.
type offlineRuntime struct {
	service        *admin.Service
	miner          *drain.PartitionedMiner
	logDetector    *detect.LogDetector
	metricDetector *detect.MetricDetector
	store          port.StateStore
}

func offlineService(ctx context.Context, configPath string, stateDir string) (*admin.Service, *detect.LogDetector, *detect.MetricDetector, port.StateStore, error) {
	runtime, err := openOffline(ctx, configPath, stateDir)
	if err != nil {
		return nil, nil, nil, nil, errors.WithStack(err)
	}

	return runtime.service, runtime.logDetector, runtime.metricDetector, runtime.store, nil
}

func openOffline(ctx context.Context, configPath string, stateDir string) (*offlineRuntime, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	store, err := fs.NewStateStore(stateDir)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	miner := drain.NewPartitionedMiner(&cfg.Logs.Drain)
	if data, err := store.Load(ctx, "drain"); err == nil {
		if err := miner.Restore(data); err != nil {
			return nil, errors.WithStack(err)
		}
	} else if !errors.Is(err, port.ErrStateNotFound) {
		return nil, errors.WithStack(err)
	}

	detector := detect.NewLogDetector(cfg.LogDetectionConfig())
	if data, err := store.Load(ctx, detector.SnapshotKey()); err == nil {
		if err := detector.Restore(data); err != nil {
			return nil, errors.WithStack(err)
		}
	} else if !errors.Is(err, port.ErrStateNotFound) {
		return nil, errors.WithStack(err)
	}

	metricDetector := detect.NewMetricDetector(cfg.MetricDetectionConfig())
	if data, err := store.Load(ctx, metricDetector.SnapshotKey()); err == nil {
		if err := metricDetector.Restore(data); err != nil {
			return nil, errors.WithStack(err)
		}
	} else if !errors.Is(err, port.ErrStateNotFound) {
		return nil, errors.WithStack(err)
	}

	return &offlineRuntime{
		service:        admin.NewService(miner, detector, metricDetector),
		miner:          miner,
		logDetector:    detector,
		metricDetector: metricDetector,
		store:          store,
	}, nil
}

func markOffline(ctx context.Context, configPath string, stateDir string, template string, marking detect.Marking) error {
	service, detector, _, store, err := offlineService(ctx, configPath, stateDir)
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
	service, _, _, store, err := offlineService(ctx, configPath, stateDir)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer store.Close()

	return service.Templates(), nil
}
