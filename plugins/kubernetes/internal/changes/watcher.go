package changes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/plugins/kubernetes/internal/api"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// DefaultServiceLabels are tried in order to derive the service
// identity from workload labels.
var DefaultServiceLabels = []string{"app.kubernetes.io/name", "app"}

// Attributes set on every change observation.
const (
	AttrNamespace = "k8s.namespace"
	AttrKind      = "k8s.kind"
	AttrName      = "k8s.name"
)

// restartedAtAnnotation is stamped on the pod template by
// kubectl rollout restart.
const restartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"

// resources are the workload kinds whose spec updates become change
// records.
var resources = []struct {
	kind   string
	plural string
}{
	{"Deployment", "deployments"},
	{"StatefulSet", "statefulsets"},
	{"DaemonSet", "daemonsets"},
}

type Options struct {
	Client *api.Client
	// Environment overrides the workload namespace as environment.
	Environment string
	// Namespaces restricts watching; empty means all namespaces.
	Namespaces []string
	// LabelSelector restricts the watched workloads.
	LabelSelector string
	// ServiceLabels are the workload labels tried in order for the
	// service identity; fallback is the workload name.
	ServiceLabels []string
}

// Watcher turns workload spec updates into change records, the
// modality the correlator attaches to subsequent anomalies
// (related_changes). Only generation bumps count — status churn during
// a rollout is ignored — and the diff is classified: image update
// (deployment), rollout restart, replicas change (scale), anything
// else (config).
type Watcher struct {
	opts *Options
	now  func() time.Time
}

func NewWatcher(opts *Options) (*Watcher, error) {
	if opts.Client == nil {
		return nil, errors.New("missing API client")
	}

	if len(opts.ServiceLabels) == 0 {
		opts.ServiceLabels = DefaultServiceLabels
	}

	return &Watcher{
		opts: opts,
		now:  time.Now,
	}, nil
}

func (w *Watcher) Ingest(ctx context.Context, out chan<- model.Observation) error {
	g, gctx := errgroup.WithContext(ctx)

	for _, resource := range resources {
		paths := []string{"/apis/apps/v1/" + resource.plural}
		if len(w.opts.Namespaces) > 0 {
			paths = paths[:0]
			for _, namespace := range w.opts.Namespaces {
				paths = append(paths, "/apis/apps/v1/namespaces/"+namespace+"/"+resource.plural)
			}
		}

		for _, path := range paths {
			g.Go(func() error {
				return w.run(gctx, resource.kind, path, out)
			})
		}
	}

	return errors.WithStack(g.Wait())
}

// workload is the subset of an apps/v1 Deployment/StatefulSet/DaemonSet
// the watcher uses.
type workload struct {
	Metadata api.ObjectMeta `json:"metadata"`
	Spec     struct {
		// Replicas is absent on DaemonSets.
		Replicas *int64 `json:"replicas"`
		Template struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

type workloadList struct {
	Items []workload `json:"items"`
}

// snapshot is what a change diff is computed against.
type snapshot struct {
	generation  int64
	replicas    int64
	hasReplicas bool
	images      map[string]string
	restartedAt string
}

func snapshotOf(target *workload) snapshot {
	snap := snapshot{
		generation:  target.Metadata.Generation,
		images:      map[string]string{},
		restartedAt: target.Spec.Template.Metadata.Annotations[restartedAtAnnotation],
	}

	if target.Spec.Replicas != nil {
		snap.replicas = *target.Spec.Replicas
		snap.hasReplicas = true
	}

	for _, container := range target.Spec.Template.Spec.Containers {
		snap.images[container.Name] = container.Image
	}

	return snap
}

// run tails one workload collection. state maps workload keys to their
// last snapshot; it is only touched from the ListWatch callbacks,
// which run sequentially.
func (w *Watcher) run(ctx context.Context, kind string, path string, out chan<- model.Observation) error {
	state := map[string]snapshot{}
	seeded := false

	query := url.Values{}
	if w.opts.LabelSelector != "" {
		query.Set("labelSelector", w.opts.LabelSelector)
	}

	return w.opts.Client.ListWatch(ctx, path, query,
		func(raw json.RawMessage) error {
			list := workloadList{}
			if err := json.Unmarshal(raw, &list); err != nil {
				return errors.WithStack(err)
			}

			// The first list is the baseline: existing workloads are not
			// replayed as changes. Later re-lists (watch expiry) diff
			// normally, catching what happened while disconnected.
			present := map[string]struct{}{}
			for _, target := range list.Items {
				present[workloadKey(&target)] = struct{}{}
				w.observe(ctx, kind, &target, state, !seeded, out)
			}
			seeded = true

			for key := range state {
				if _, exists := present[key]; !exists {
					delete(state, key)
				}
			}

			return nil
		},
		func(event *api.WatchEvent) error {
			target := workload{}
			if err := json.Unmarshal(event.Object, &target); err != nil {
				slog.WarnContext(ctx, "malformed workload watch event", slog.Any("error", err))
				return nil
			}

			if event.Type == api.Deleted {
				delete(state, workloadKey(&target))
				return nil
			}

			w.observe(ctx, kind, &target, state, false, out)

			return nil
		})
}

func workloadKey(target *workload) string {
	if target.Metadata.UID != "" {
		return target.Metadata.UID
	}

	return target.Metadata.Namespace + "/" + target.Metadata.Name
}

func (w *Watcher) observe(ctx context.Context, kind string, target *workload, state map[string]snapshot, seed bool, out chan<- model.Observation) {
	key := workloadKey(target)
	next := snapshotOf(target)

	prev, known := state[key]
	state[key] = next

	if !known {
		if seed {
			return
		}

		// A workload appearing after the baseline is a deployment.
		record := model.ChangeRecord{
			Type:    "deployment",
			Summary: kind + " " + target.Metadata.Name + " created",
		}
		if image, exists := firstImage(target); exists {
			record.Version = imageTag(image)
		}

		w.emit(ctx, out, kind, target, record)

		return
	}

	// Status churn (rollout progress, replica counts) does not bump the
	// generation: no change.
	if next.generation == prev.generation {
		return
	}

	record := w.classify(kind, target, prev, next)
	w.emit(ctx, out, kind, target, record)
}

// classify names what a generation bump was about.
func (w *Watcher) classify(kind string, target *workload, prev snapshot, next snapshot) model.ChangeRecord {
	updated := []string{}
	version := ""
	for _, container := range target.Spec.Template.Spec.Containers {
		before, existed := prev.images[container.Name]
		if !existed || before == container.Image {
			continue
		}

		updated = append(updated, "image "+container.Name+": "+before+" -> "+container.Image)
		// The shortened image (base name and tag, checkout:v1.8.2) is
		// the same shape as the versions the CI examples report.
		if version == "" {
			version = imageTag(container.Image)
		}
	}

	prefix := kind + " " + target.Metadata.Name + ": "

	switch {
	case len(updated) > 0:
		return model.ChangeRecord{
			Type:    "deployment",
			Version: version,
			Summary: prefix + strings.Join(updated, ", "),
		}

	case next.restartedAt != prev.restartedAt:
		return model.ChangeRecord{
			Type:    "restart",
			Summary: prefix + "rollout restart",
		}

	case next.hasReplicas && prev.hasReplicas && next.replicas != prev.replicas:
		return model.ChangeRecord{
			Type:    "scale",
			Summary: prefix + fmt.Sprintf("replicas %d -> %d", prev.replicas, next.replicas),
		}

	default:
		return model.ChangeRecord{
			Type:    "config",
			Summary: prefix + fmt.Sprintf("spec update (generation %d)", next.generation),
		}
	}
}

func (w *Watcher) emit(ctx context.Context, out chan<- model.Observation, kind string, target *workload, record model.ChangeRecord) {
	environment := w.opts.Environment
	if environment == "" {
		environment = target.Metadata.Namespace
	}

	now := w.now()

	obs := model.Observation{
		ID:          model.NewID(),
		Service:     w.service(target),
		Environment: environment,
		Modality:    model.ModalityChange,
		Timestamp:   now,
		IngestedAt:  now,
		Attributes: map[string]string{
			AttrNamespace: target.Metadata.Namespace,
			AttrKind:      kind,
			AttrName:      target.Metadata.Name,
		},
		Change: &record,
	}

	select {
	case out <- obs:
	case <-ctx.Done():
	}
}

func (w *Watcher) service(target *workload) string {
	for _, label := range w.opts.ServiceLabels {
		if value := target.Metadata.Labels[label]; value != "" {
			return value
		}
	}

	return target.Metadata.Name
}

func firstImage(target *workload) (string, bool) {
	for _, container := range target.Spec.Template.Spec.Containers {
		if container.Image != "" {
			return container.Image, true
		}
	}

	return "", false
}

// imageTag shortens a full image reference to its repository base name
// and tag: ghcr.io/acme/checkout:v1.8.2 -> checkout:v1.8.2.
func imageTag(image string) string {
	if index := strings.LastIndex(image, "/"); index >= 0 {
		return image[index+1:]
	}

	return image
}
