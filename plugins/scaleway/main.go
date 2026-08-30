// tezcatl-source-scaleway ingests the logs and metrics of Scaleway
// serverless containers.
//
// The scw CLI is used for discovery only: which containers exist, and
// where the Cockpit endpoints are. It already holds the operator's
// credentials, so tezcatl is never told them. The data itself comes
// from Cockpit, since the released CLI cannot stream container logs:
// Loki for logs, Prometheus for metrics, both behind a Cockpit token
// with read_only_logs and read_only_metrics scopes.
//
// Configuration (JSON):
//
//	{
//	  "token": "${SCW_COCKPIT_TOKEN}", // required, never inline it
//	  "region": "fr-par",              // defaults to the scw profile
//	  "project_id": "",                // restrict to one project
//	  "profile": "",                   // scw profile to use
//	  "scw_path": "scw",               // CLI to invoke
//	  "logs_url": "",                  // discovered when empty
//	  "metrics_url": "",               // discovered when empty
//	  "interval": "30s",               // polling period
//	  "lookback": "5m",                // how far back the first poll goes
//	  "refresh_interval": "5m",        // how often the container list is refreshed
//	  "environment": "",               // overrides the namespace name
//	  "no_logs": false,
//	  "no_metrics": false
//	}
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	grpcadapter "github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/core/model"
	sdk "github.com/bornholm/tezcatl/pkg/plugin"
	"github.com/bornholm/tezcatl/plugins/scaleway/internal/cockpit"
	"github.com/bornholm/tezcatl/plugins/scaleway/internal/scw"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

type config struct {
	Token           string `json:"token"`
	Region          string `json:"region"`
	ProjectID       string `json:"project_id"`
	Profile         string `json:"profile"`
	ScwPath         string `json:"scw_path"`
	LogsURL         string `json:"logs_url"`
	MetricsURL      string `json:"metrics_url"`
	Interval        string `json:"interval"`
	Lookback        string `json:"lookback"`
	RefreshInterval string `json:"refresh_interval"`
	Environment     string `json:"environment"`
	NoLogs          bool   `json:"no_logs"`
	NoMetrics       bool   `json:"no_metrics"`
}

const (
	defaultInterval        = 30 * time.Second
	defaultLookback        = 5 * time.Minute
	defaultRefreshInterval = 5 * time.Minute
	// containerSelector matches every serverless container of the
	// account; Cockpit also stores logs of other Scaleway products.
	containerSelector = `{resource_type="serverless_container"}`
)

func main() {
	sdk.Serve(sdk.SourceFunc(stream))
}

func stream(ctx context.Context, rawConfig []byte, emit sdk.EmitFunc) error {
	cfg := config{}
	if len(rawConfig) > 0 {
		if err := json.Unmarshal(rawConfig, &cfg); err != nil {
			return errors.Wrap(err, "malformed plugin configuration")
		}
	}

	if cfg.Token == "" {
		return errors.New("token is required: create one with 'scw cockpit token create name=tezcatl token-scopes.0=read_only_logs token-scopes.1=read_only_metrics' and pass it through the environment")
	}

	interval, err := duration(cfg.Interval, defaultInterval)
	if err != nil {
		return errors.Wrap(err, "malformed interval")
	}

	lookback, err := duration(cfg.Lookback, defaultLookback)
	if err != nil {
		return errors.Wrap(err, "malformed lookback")
	}

	refresh, err := duration(cfg.RefreshInterval, defaultRefreshInterval)
	if err != nil {
		return errors.Wrap(err, "malformed refresh_interval")
	}

	cli := scw.NewCLI(cfg.ScwPath, cfg.Profile, cfg.Region)

	directory := newDirectory(cli, cfg.ProjectID, cfg.Environment)
	if err := directory.refresh(ctx); err != nil {
		return errors.Wrap(err, "could not discover containers")
	}

	logsURL, metricsURL := cfg.LogsURL, cfg.MetricsURL
	if logsURL == "" || metricsURL == "" {
		sources, err := cli.DataSources(ctx, cfg.ProjectID)
		if err != nil {
			return errors.Wrap(err, "could not discover the cockpit endpoints")
		}

		if logsURL == "" {
			logsURL = scw.Endpoint(sources, "logs")
		}

		if metricsURL == "" {
			metricsURL = scw.Endpoint(sources, "metrics")
		}
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error { return directory.run(gctx, refresh) })

	if !cfg.NoLogs {
		if logsURL == "" {
			return errors.New("no cockpit logs endpoint found; set logs_url or disable logs with no_logs")
		}

		logs := cockpit.NewLogClient(logsURL, cfg.Token, nil)
		logs.Since(time.Now().Add(-lookback))

		g.Go(func() error { return streamLogs(gctx, logs, directory, interval, emit) })
	}

	if !cfg.NoMetrics {
		if metricsURL == "" {
			return errors.New("no cockpit metrics endpoint found; set metrics_url or disable metrics with no_metrics")
		}

		metrics := cockpit.NewMetricClient(metricsURL, cfg.Token, nil)

		g.Go(func() error { return streamMetrics(gctx, metrics, directory, interval, emit) })
	}

	return errors.WithStack(g.Wait())
}

func streamLogs(ctx context.Context, client *cockpit.LogClient, directory *directory, interval time.Duration, emit sdk.EmitFunc) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		entries, err := client.Poll(ctx, containerSelector, 0, time.Now())
		if err != nil {
			if ctx.Err() != nil {
				return errors.WithStack(ctx.Err())
			}

			slog.Warn("could not read cockpit logs", slog.Any("error", err))
		}

		for _, entry := range entries {
			service, environment := directory.identify(entry.ResourceID, entry.ResourceName)

			obs := model.Observation{
				Source:      environment + "/" + service,
				Service:     service,
				Environment: environment,
				Modality:    model.ModalityLog,
				Timestamp:   entry.Timestamp,
				Log: &model.LogRecord{
					Raw: entry.Raw,
					// The plugin knows the envelope Scaleway wraps
					// around each line, so it unwraps it here rather
					// than leaving the server to guess.
					Message: entry.Message,
					Level:   entry.Level,
				},
				Attributes: attributes(entry),
			}

			if err := emit(grpcadapter.ToProtoObservation(&obs)); err != nil {
				return errors.WithStack(err)
			}
		}

		select {
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		case <-ticker.C:
		}
	}
}

func attributes(entry cockpit.Entry) map[string]string {
	attrs := map[string]string{}

	if entry.Instance != "" {
		attrs["scaleway.instance"] = entry.Instance
	}

	if entry.ResourceID != "" {
		attrs["scaleway.container_id"] = entry.ResourceID
	}

	if entry.Region != "" {
		attrs["scaleway.region"] = entry.Region
	}

	if entry.Stream != "" {
		attrs["scaleway.stream"] = entry.Stream
	}

	if len(attrs) == 0 {
		return nil
	}

	return attrs
}

func streamMetrics(ctx context.Context, client *cockpit.MetricClient, directory *directory, interval time.Duration, emit sdk.EmitFunc) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	queries := cockpit.DefaultQueries()

	for {
		samples, err := client.Collect(ctx, queries)
		if err != nil {
			if ctx.Err() != nil {
				return errors.WithStack(ctx.Err())
			}

			slog.Warn("could not read cockpit metrics", slog.Any("error", err))
		}

		for _, sample := range samples {
			service, environment := directory.identify(sample.ResourceID, "")

			obs := model.Observation{
				Source:      environment + "/" + service,
				Service:     service,
				Environment: environment,
				Modality:    model.ModalityMetric,
				Timestamp:   sample.Timestamp,
				Metric: &model.MetricSample{
					Name:  sample.Metric,
					Value: sample.Value,
				},
			}

			if err := emit(grpcadapter.ToProtoObservation(&obs)); err != nil {
				return errors.WithStack(err)
			}
		}

		select {
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		case <-ticker.C:
		}
	}
}

func duration(raw string, fallback time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, errors.WithStack(err)
	}

	return parsed, nil
}
