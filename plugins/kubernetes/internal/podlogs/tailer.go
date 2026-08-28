package podlogs

import (
	"bufio"
	"context"
	"encoding/json"
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
// identity from pod labels.
var DefaultServiceLabels = []string{"app.kubernetes.io/name", "app"}

// Attributes set on every pod log observation.
const (
	AttrNamespace = "k8s.namespace"
	AttrPod       = "k8s.pod"
	AttrContainer = "k8s.container"
)

const (
	// initialSinceSeconds bounds the history replayed when a stream is
	// first attached to an already-running container.
	initialSinceSeconds = "10"

	streamInitialBackoff = time.Second
	streamMaxBackoff     = 30 * time.Second

	// maxLineBytes caps buffered log lines; longer ones abort and
	// restart the stream.
	maxLineBytes = 1024 * 1024
)

type Options struct {
	Client *api.Client
	// Environment overrides the pod namespace as environment.
	Environment string
	// Namespaces restricts watching; empty means all namespaces.
	Namespaces []string
	// LabelSelector restricts the watched pods (app=checkout).
	LabelSelector string
	// ServiceLabels are the pod labels tried in order for the service
	// identity; fallback is the controller owner name (ReplicaSet names
	// are trimmed of their pod-template-hash), then the pod name.
	ServiceLabels []string
}

// Tailer follows the logs of every running pod: a pods list+watch
// discovers them (including pods created later — the kubectl selector
// limitation does not apply), and one follow stream per container
// ships each line as a log observation. Streams reconnect while their
// pod is alive, so container restarts are picked up.
type Tailer struct {
	opts *Options
	now  func() time.Time
}

func NewTailer(opts *Options) (*Tailer, error) {
	if opts.Client == nil {
		return nil, errors.New("missing API client")
	}

	if len(opts.ServiceLabels) == 0 {
		opts.ServiceLabels = DefaultServiceLabels
	}

	return &Tailer{
		opts: opts,
		now:  time.Now,
	}, nil
}

func (t *Tailer) Ingest(ctx context.Context, out chan<- model.Observation) error {
	paths := []string{"/api/v1/pods"}
	if len(t.opts.Namespaces) > 0 {
		paths = paths[:0]
		for _, namespace := range t.opts.Namespaces {
			paths = append(paths, "/api/v1/namespaces/"+namespace+"/pods")
		}
	}

	g, gctx := errgroup.WithContext(ctx)

	for _, path := range paths {
		g.Go(func() error {
			return t.run(gctx, path, out)
		})
	}

	return errors.WithStack(g.Wait())
}

// pod is the subset of a core/v1 Pod the tailer uses.
type pod struct {
	Metadata api.ObjectMeta `json:"metadata"`
	Spec     struct {
		Containers []struct {
			Name string `json:"name"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type podList struct {
	Items []pod `json:"items"`
}

// run tails one pods collection. streams maps ns/pod/container to the
// cancel function of its follow goroutine; it is only touched from the
// ListWatch callbacks, which run sequentially.
func (t *Tailer) run(ctx context.Context, path string, out chan<- model.Observation) error {
	streams := map[string]context.CancelFunc{}

	query := url.Values{}
	if t.opts.LabelSelector != "" {
		query.Set("labelSelector", t.opts.LabelSelector)
	}

	return t.opts.Client.ListWatch(ctx, path, query,
		func(raw json.RawMessage) error {
			list := podList{}
			if err := json.Unmarshal(raw, &list); err != nil {
				return errors.WithStack(err)
			}

			t.reconcile(ctx, streams, list.Items, out)

			return nil
		},
		func(event *api.WatchEvent) error {
			target := pod{}
			if err := json.Unmarshal(event.Object, &target); err != nil {
				slog.WarnContext(ctx, "malformed pod watch event", slog.Any("error", err))
				return nil
			}

			if event.Type == api.Deleted {
				t.stop(streams, &target)
			} else {
				t.sync(ctx, streams, &target, out)
			}

			return nil
		})
}

// reconcile aligns the streams with a full pods list: streams of pods
// that are gone are canceled, missing ones are started.
func (t *Tailer) reconcile(ctx context.Context, streams map[string]context.CancelFunc, pods []pod, out chan<- model.Observation) {
	wanted := map[string]struct{}{}

	for _, target := range pods {
		if target.Status.Phase == "Running" {
			for _, container := range target.Spec.Containers {
				wanted[streamKey(&target, container.Name)] = struct{}{}
			}
		}
	}

	for key, cancel := range streams {
		if _, exists := wanted[key]; !exists {
			cancel()
			delete(streams, key)
		}
	}

	for _, target := range pods {
		t.sync(ctx, streams, &target, out)
	}
}

// sync starts the follow streams of a running pod and stops those of a
// terminated one (Succeeded/Failed).
func (t *Tailer) sync(ctx context.Context, streams map[string]context.CancelFunc, target *pod, out chan<- model.Observation) {
	if target.Status.Phase != "Running" {
		if target.Status.Phase == "Succeeded" || target.Status.Phase == "Failed" {
			t.stop(streams, target)
		}

		return
	}

	service := t.service(target)

	for _, container := range target.Spec.Containers {
		key := streamKey(target, container.Name)
		if _, exists := streams[key]; exists {
			continue
		}

		streamCtx, cancel := context.WithCancel(ctx)
		streams[key] = cancel

		go t.follow(streamCtx, target.Metadata.Namespace, target.Metadata.Name, container.Name, service, out)
	}
}

func (t *Tailer) stop(streams map[string]context.CancelFunc, target *pod) {
	for _, container := range target.Spec.Containers {
		key := streamKey(target, container.Name)
		if cancel, exists := streams[key]; exists {
			cancel()
			delete(streams, key)
		}
	}
}

func streamKey(target *pod, container string) string {
	return target.Metadata.Namespace + "/" + target.Metadata.Name + "/" + container
}

// follow streams one container's logs until its context is canceled,
// reconnecting when the stream ends (container stopped, restarting…).
// Reconnections resume from the last seen timestamp; sinceTime has
// second precision, so a line straddling the boundary may be shipped
// twice — deduplication is not worth the state.
func (t *Tailer) follow(ctx context.Context, namespace string, podName string, container string, service string, out chan<- model.Observation) {
	backoff := streamInitialBackoff
	lastTimestamp := time.Time{}

	for {
		query := url.Values{}
		query.Set("follow", "true")
		query.Set("container", container)
		// The kubelet prefixes every line with an RFC3339Nano timestamp;
		// the pipeline's parse stage extracts it, the tailer tracks it
		// for reconnections.
		query.Set("timestamps", "true")

		if lastTimestamp.IsZero() {
			query.Set("sinceSeconds", initialSinceSeconds)
		} else {
			query.Set("sinceTime", lastTimestamp.Format(time.RFC3339))
		}

		path := "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods/" + url.PathEscape(podName) + "/log?" + query.Encode()

		body, err := t.opts.Client.Stream(ctx, path)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			slog.WarnContext(ctx, "could not stream pod logs", slog.String("pod", namespace+"/"+podName), slog.String("container", container), slog.Any("error", err))

			if sleep(ctx, backoff) != nil {
				return
			}
			backoff = min(backoff*2, streamMaxBackoff)

			continue
		}

		start := t.now()

		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 64*1024), maxLineBytes)

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			if timestamp := t.emit(ctx, out, namespace, podName, container, service, line); !timestamp.IsZero() {
				lastTimestamp = timestamp
			}
		}

		body.Close()

		if ctx.Err() != nil {
			return
		}

		// A stream that lasted resets the backoff.
		if t.now().Sub(start) > time.Minute {
			backoff = streamInitialBackoff
		}

		if sleep(ctx, backoff) != nil {
			return
		}
		backoff = min(backoff*2, streamMaxBackoff)
	}
}

func (t *Tailer) emit(ctx context.Context, out chan<- model.Observation, namespace string, podName string, container string, service string, line string) time.Time {
	timestamp := time.Time{}
	if token, _, found := strings.Cut(line, " "); found {
		if parsed, err := time.Parse(time.RFC3339Nano, token); err == nil {
			timestamp = parsed
		}
	}

	environment := t.opts.Environment
	if environment == "" {
		environment = namespace
	}

	obs := model.Observation{
		ID:          model.NewID(),
		Service:     service,
		Environment: environment,
		Modality:    model.ModalityLog,
		Timestamp:   timestamp,
		IngestedAt:  t.now(),
		Attributes: map[string]string{
			AttrNamespace: namespace,
			AttrPod:       podName,
			AttrContainer: container,
		},
		// Raw keeps the timestamp prefix: the pipeline's parse stage
		// strips it and unwraps any JSON envelope underneath.
		Log: &model.LogRecord{Raw: line},
	}

	select {
	case out <- obs:
	case <-ctx.Done():
	}

	return timestamp
}

func (t *Tailer) service(target *pod) string {
	for _, label := range t.opts.ServiceLabels {
		if value := target.Metadata.Labels[label]; value != "" {
			return value
		}
	}

	for _, owner := range target.Metadata.OwnerReferences {
		if !owner.Controller {
			continue
		}

		name := owner.Name

		// ReplicaSet names carry the deployment's pod-template-hash
		// suffix: checkout-7d9f8b -> checkout.
		if owner.Kind == "ReplicaSet" {
			if index := strings.LastIndex(name, "-"); index > 0 {
				name = name[:index]
			}
		}

		return name
	}

	return target.Metadata.Name
}

func sleep(ctx context.Context, duration time.Duration) error {
	select {
	case <-time.After(duration):
		return nil
	case <-ctx.Done():
		return errors.WithStack(ctx.Err())
	}
}
