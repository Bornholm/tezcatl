// tezcatl-source-prometheus polls a Prometheus HTTP API: every PromQL
// instant query is evaluated at each interval and every result sample
// becomes a metric observation.
//
// Configuration (JSON):
//
//	{
//	  "url": "http://127.0.0.1:9090",
//	  "interval": "30s",
//	  "service": "…",                 // default identity of polled metrics
//	  "environment": "production",
//	  "queries": [
//	    {
//	      "name": "latency_p95_s",
//	      "query": "histogram_quantile(0.95, …)",
//	      "service": "…",             // per-query identity override
//	      "environment": "…",
//	      "service_label": "service"  // derive the service from a sample label
//	    }
//	  ]
//	}
package main

import (
	"context"
	"encoding/json"
	"time"

	grpcadapter "github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/core/model"
	sdk "github.com/bornholm/tezcatl/pkg/plugin"
	"github.com/bornholm/tezcatl/plugins/prometheus/internal/poller"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

type config struct {
	URL         string         `json:"url"`
	Interval    string         `json:"interval"`
	Service     string         `json:"service"`
	Environment string         `json:"environment"`
	Queries     []poller.Query `json:"queries"`
}

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

	interval := time.Duration(0)
	if cfg.Interval != "" {
		parsed, err := time.ParseDuration(cfg.Interval)
		if err != nil {
			return errors.Wrapf(err, "malformed interval %q", cfg.Interval)
		}
		interval = parsed
	}

	source, err := poller.NewPoller(&poller.Options{
		URL:         cfg.URL,
		Interval:    interval,
		Service:     cfg.Service,
		Environment: cfg.Environment,
		Queries:     cfg.Queries,
	})
	if err != nil {
		return errors.WithStack(err)
	}

	observations := make(chan model.Observation, 256)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return source.Ingest(gctx, observations)
	})

	g.Go(func() error {
		for {
			select {
			case obs := <-observations:
				if err := emit(grpcadapter.ToProtoObservation(&obs)); err != nil {
					return errors.WithStack(err)
				}
			case <-gctx.Done():
				return errors.WithStack(gctx.Err())
			}
		}
	})

	return errors.WithStack(g.Wait())
}
