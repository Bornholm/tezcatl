// tezcatl-source-kubernetes streams a Kubernetes cluster straight from
// the API server (no kubectl, no client-go): cluster events as a log
// flux (same JSON envelope as the kubectl tutorial), pod logs (pods
// list+watch, one follow stream per container — pods created after
// startup are picked up) and workload spec updates as change records
// (image updates, rollout restarts, scaling — correlated to the
// anomalies that follow).
//
// Configuration (JSON, all fields optional when running in-cluster):
//
//	{
//	  "api_server": "https://…",        // overrides the kubeconfig server
//	  "kubeconfig": "~/.kube/config",   // kubectl configuration file
//	  "context": "…",                   // default: current-context
//	  "token": "…",                     // or token_file; default: serviceaccount
//	  "token_file": "…",
//	  "ca_file": "…",
//	  "insecure_skip_verify": false,
//	  "environment": "prod",            // default: the object namespace
//	  "namespaces": ["default"],        // default: all namespaces
//	  "label_selector": "app=checkout", // pods only
//	  "service_labels": ["app.kubernetes.io/name", "app"],
//	  "events_service": "k8s-events",
//	  "no_events": false,
//	  "no_logs": false,
//	  "no_changes": false
//	}
package main

import (
	"context"
	"encoding/json"

	grpcadapter "github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
	sdk "github.com/bornholm/tezcatl/pkg/plugin"
	"github.com/bornholm/tezcatl/plugins/kubernetes/internal/api"
	"github.com/bornholm/tezcatl/plugins/kubernetes/internal/changes"
	"github.com/bornholm/tezcatl/plugins/kubernetes/internal/events"
	"github.com/bornholm/tezcatl/plugins/kubernetes/internal/podlogs"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

type config struct {
	APIServer          string   `json:"api_server"`
	Kubeconfig         string   `json:"kubeconfig"`
	Context            string   `json:"context"`
	Token              string   `json:"token"`
	TokenFile          string   `json:"token_file"`
	CAFile             string   `json:"ca_file"`
	InsecureSkipVerify bool     `json:"insecure_skip_verify"`
	Environment        string   `json:"environment"`
	Namespaces         []string `json:"namespaces"`
	LabelSelector      string   `json:"label_selector"`
	ServiceLabels      []string `json:"service_labels"`
	EventsService      string   `json:"events_service"`
	NoEvents           bool     `json:"no_events"`
	NoLogs             bool     `json:"no_logs"`
	NoChanges          bool     `json:"no_changes"`
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

	client, err := api.NewClient(&api.Config{
		Server:             cfg.APIServer,
		Kubeconfig:         cfg.Kubeconfig,
		Context:            cfg.Context,
		Token:              cfg.Token,
		TokenFile:          cfg.TokenFile,
		CAFile:             cfg.CAFile,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	})
	if err != nil {
		return errors.WithStack(err)
	}

	ingesters := []port.Ingester{}

	if !cfg.NoEvents {
		watcher, err := events.NewWatcher(&events.Options{
			Client:      client,
			Service:     cfg.EventsService,
			Environment: cfg.Environment,
			Namespaces:  cfg.Namespaces,
		})
		if err != nil {
			return errors.WithStack(err)
		}

		ingesters = append(ingesters, watcher)
	}

	if !cfg.NoLogs {
		tailer, err := podlogs.NewTailer(&podlogs.Options{
			Client:        client,
			Environment:   cfg.Environment,
			Namespaces:    cfg.Namespaces,
			LabelSelector: cfg.LabelSelector,
			ServiceLabels: cfg.ServiceLabels,
		})
		if err != nil {
			return errors.WithStack(err)
		}

		ingesters = append(ingesters, tailer)
	}

	if !cfg.NoChanges {
		watcher, err := changes.NewWatcher(&changes.Options{
			Client:        client,
			Environment:   cfg.Environment,
			Namespaces:    cfg.Namespaces,
			LabelSelector: cfg.LabelSelector,
			ServiceLabels: cfg.ServiceLabels,
		})
		if err != nil {
			return errors.WithStack(err)
		}

		ingesters = append(ingesters, watcher)
	}

	if len(ingesters) == 0 {
		return errors.New("all sources are disabled")
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
