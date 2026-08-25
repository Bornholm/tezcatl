// tezcatl-source-host collects metrics of the local machine (CPU,
// memory, load, disk via /proc — Linux) and of its Docker containers
// (CPU, memory, running count per service via the Engine API).
//
// Configuration (JSON, all fields optional):
//
//	{
//	  "interval": "30s",
//	  "service": "host",            // identity of the host metrics
//	  "environment": "production",
//	  "disk_paths": ["/"],
//	  "docker_socket": "/var/run/docker.sock",
//	  "service_label": "com.dokku.app-name",
//	  "no_system": false,
//	  "no_docker": false
//	}
package main

import (
	"context"
	"encoding/json"
	"time"

	grpcadapter "github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
	sdk "github.com/bornholm/tezcatl/pkg/plugin"
	"github.com/bornholm/tezcatl/plugins/host/internal/docker"
	"github.com/bornholm/tezcatl/plugins/host/internal/system"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

type config struct {
	Interval     string   `json:"interval"`
	Service      string   `json:"service"`
	Environment  string   `json:"environment"`
	DiskPaths    []string `json:"disk_paths"`
	DockerSocket string   `json:"docker_socket"`
	ServiceLabel string   `json:"service_label"`
	NoSystem     bool     `json:"no_system"`
	NoDocker     bool     `json:"no_docker"`
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

	interval := 30 * time.Second
	if cfg.Interval != "" {
		parsed, err := time.ParseDuration(cfg.Interval)
		if err != nil {
			return errors.Wrapf(err, "malformed interval %q", cfg.Interval)
		}
		interval = parsed
	}

	ingesters := []port.Ingester{}

	if !cfg.NoSystem {
		collector, err := system.NewCollector(&system.Options{
			Interval:    interval,
			Service:     cfg.Service,
			Environment: cfg.Environment,
			DiskPaths:   cfg.DiskPaths,
		})
		if err != nil {
			return errors.WithStack(err)
		}

		ingesters = append(ingesters, collector)
	}

	if !cfg.NoDocker {
		collector, err := docker.NewCollector(&docker.Options{
			Socket:       cfg.DockerSocket,
			Interval:     interval,
			Environment:  cfg.Environment,
			ServiceLabel: cfg.ServiceLabel,
		})
		if err != nil {
			return errors.WithStack(err)
		}

		ingesters = append(ingesters, collector)
	}

	if len(ingesters) == 0 {
		return errors.New("both collectors are disabled")
	}

	observations := make(chan model.Observation, 256)

	g, gctx := errgroup.WithContext(ctx)

	for _, ingester := range ingesters {
		g.Go(func() error {
			return ingester.Ingest(gctx, observations)
		})
	}

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
