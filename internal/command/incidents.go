package command

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/core/incident"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func NewIncidentsCommand() *cli.Command {
	return &cli.Command{
		Name:  "incidents",
		Usage: "Assemble the anomalies of a period into incident briefings: trigger, spread, evidence, correlated changes",
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
				Usage: "how far back to look: a duration (24h) or an RFC3339 timestamp",
				Value: "24h",
			},
			&cli.DurationFlag{
				Name:  "gap",
				Usage: "a lull longer than this ends an incident",
				Value: incident.DefaultGap,
			},
			&cli.DurationFlag{
				Name:  "max-duration",
				Usage: "an incident never runs longer than this, however related the events",
				Value: incident.DefaultMaxDuration,
			},
			&cli.DurationFlag{
				Name:  "co-occurrence",
				Usage: "how close two unrelated services must anomalize to count as one event spreading",
				Value: incident.DefaultCoOccurrence,
			},
			&cli.StringFlag{
				Name:  "format",
				Usage: "text or json",
				Value: "text",
			},
		},
		Action: func(ctx *cli.Context) error {
			format := ctx.String("format")
			if format != "text" && format != "json" {
				return errors.Errorf("unknown format %q (text, json)", format)
			}

			since, err := parseTimeFlag(ctx.String("since"))
			if err != nil {
				return errors.Wrap(err, "malformed --since")
			}

			conn, err := grpc.Dial(ctx.String("target"), ctx.String("tls-ca"))
			if err != nil {
				return errors.WithStack(err)
			}

			defer conn.Close()

			req := &tezcatlv1.ListEventsRequest{}
			if !since.IsZero() {
				req.Since = since.Format(time.RFC3339Nano)
			}

			res, err := tezcatlv1.NewAdminServiceClient(conn).ListEvents(ctx.Context, req)
			if err != nil {
				return errors.WithStack(err)
			}

			events := make([]model.Event, 0, len(res.GetEvents()))
			for _, envelope := range res.GetEvents() {
				var event model.Event
				if err := json.Unmarshal([]byte(envelope.GetJson()), &event); err != nil {
					return errors.WithStack(err)
				}

				// The incident story is made of anomalies; debug and
				// informational kinds would drown it.
				if strings.HasPrefix(event.Kind, "anomaly.") {
					events = append(events, event)
				}
			}

			incidents := incident.Group(events, incident.Options{
				Gap:          ctx.Duration("gap"),
				MaxDuration:  ctx.Duration("max-duration"),
				CoOccurrence: ctx.Duration("co-occurrence"),
			})

			if format == "json" {
				encoder := json.NewEncoder(os.Stdout)
				for _, entry := range incidents {
					if err := encoder.Encode(entry); err != nil {
						return errors.WithStack(err)
					}
				}

				return nil
			}

			if len(incidents) == 0 {
				fmt.Println("no incident: no anomaly event in the period")

				return nil
			}

			for i, entry := range incidents {
				if i > 0 {
					fmt.Println(strings.Repeat("-", 72))
				}

				incident.Render(os.Stdout, entry)
			}

			return nil
		},
	}
}
