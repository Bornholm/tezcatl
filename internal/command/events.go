package command

import (
	"fmt"
	"os"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func NewEventsCommand() *cli.Command {
	return &cli.Command{
		Name:  "events",
		Usage: "Print past events from the server's local event log, one JSON document per line",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "target",
				Usage:   "running server to talk to, e.g. tcp://host:4242",
				Value:   defaultTarget,
				EnvVars: []string{"TEZCATL_TARGET"},
			},
			&cli.StringFlag{
				Name:  "tls-ca",
				Usage: "PEM CA bundle verifying a tls:// target (default: system roots)",
			},
			&cli.StringFlag{
				Name:  "since",
				Usage: "oldest event to include: a duration back from now (1h, 30m) or an RFC3339 timestamp",
			},
			&cli.StringFlag{
				Name:  "until",
				Usage: "newest event to include, same forms as --since",
			},
			&cli.IntFlag{
				Name:  "limit",
				Usage: "keep only the newest events (0: server default)",
			},
		},
		Action: func(ctx *cli.Context) error {
			since, err := parseTimeFlag(ctx.String("since"))
			if err != nil {
				return errors.Wrap(err, "malformed --since")
			}

			until, err := parseTimeFlag(ctx.String("until"))
			if err != nil {
				return errors.Wrap(err, "malformed --until")
			}

			conn, err := grpc.Dial(ctx.String("target"), ctx.String("tls-ca"))
			if err != nil {
				return errors.WithStack(err)
			}

			defer conn.Close()

			req := &tezcatlv1.ListEventsRequest{Limit: int32(ctx.Int("limit"))}
			if !since.IsZero() {
				req.Since = since.Format(time.RFC3339Nano)
			}
			if !until.IsZero() {
				req.Until = until.Format(time.RFC3339Nano)
			}

			res, err := tezcatlv1.NewAdminServiceClient(conn).ListEvents(ctx.Context, req)
			if err != nil {
				return errors.WithStack(err)
			}

			for _, envelope := range res.GetEvents() {
				fmt.Fprintln(os.Stdout, envelope.GetJson())
			}

			return nil
		},
	}
}

// parseTimeFlag accepts a duration back from now ("1h") or an RFC3339
// timestamp; empty means unbounded.
func parseTimeFlag(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}

	if age, err := time.ParseDuration(raw); err == nil {
		return time.Now().Add(-age), nil
	}

	bound, err := time.Parse(time.RFC3339Nano, raw)

	return bound, errors.WithStack(err)
}
