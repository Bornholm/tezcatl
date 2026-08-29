package command

import (
	"context"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/build"
	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/tui"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func NewTopCommand() *cli.Command {
	return &cli.Command{
		Name:  "top",
		Usage: "Interactive terminal view of learned templates and metric baselines",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "target",
				Usage:    "running server to talk to, e.g. unix:///run/tezcatl.sock",
				Required: true,
			},
			&cli.StringFlag{
				Name:  "tls-ca",
				Usage: "PEM CA bundle verifying a tls:// target (default: system roots)",
			},
			&cli.DurationFlag{
				Name:  "refresh",
				Usage: "interval between data refreshes",
				Value: 3 * time.Second,
			},
		},
		Action: func(ctx *cli.Context) error {
			target := ctx.String("target")

			conn, err := grpc.Dial(target, ctx.String("tls-ca"))
			if err != nil {
				return errors.WithStack(err)
			}

			defer conn.Close()

			source := &grpcSource{client: tezcatlv1.NewAdminServiceClient(conn)}

			return errors.WithStack(tui.Run(ctx.Context, source, tui.Options{
				Target:  target,
				Version: build.LongVersion,
				Refresh: ctx.Duration("refresh"),
			}))
		},
	}
}

// grpcSource feeds the top view from a live AdminService over a single
// persistent connection.
type grpcSource struct {
	client tezcatlv1.AdminServiceClient
}

func (s *grpcSource) Templates(ctx context.Context) ([]admin.TemplateInfo, error) {
	res, err := s.client.ListTemplates(ctx, &tezcatlv1.ListTemplatesRequest{})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return templatesFromProto(res.GetTemplates()), nil
}

func (s *grpcSource) Metrics(ctx context.Context) ([]detect.SeriesInfo, error) {
	res, err := s.client.ListMetrics(ctx, &tezcatlv1.ListMetricsRequest{})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return seriesFromProto(res.GetMetrics()), nil
}

func (s *grpcSource) Mark(ctx context.Context, template string, marking detect.Marking) error {
	if _, err := s.client.MarkTemplate(ctx, &tezcatlv1.MarkTemplateRequest{
		Template: template,
		Marking:  string(marking),
	}); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
