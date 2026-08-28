package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/plugins/kubernetes/internal/api"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// DefaultService is the identity of the events stream: cluster events
// are one log flux, whatever object they concern.
const DefaultService = "k8s-events"

// Attributes set on every event observation.
const (
	AttrNamespace = "k8s.namespace"
	AttrKind      = "k8s.kind"
	AttrName      = "k8s.name"
	AttrReason    = "k8s.reason"
)

type Options struct {
	Client *api.Client
	// Service is the identity of the events stream.
	Service string
	// Environment overrides the event namespace as environment.
	Environment string
	// Namespaces restricts watching; empty means all namespaces.
	Namespaces []string
}

// Watcher turns cluster events (BackOff, Killing, FailedScheduling…)
// into log observations. Each event becomes the same minimal JSON
// envelope as the kubectl tutorial ({time, level, msg}), which the
// pipeline's parse stage unwraps; template mining then surveils the
// novelty and frequency of each event type.
type Watcher struct {
	opts *Options
	now  func() time.Time
}

func NewWatcher(opts *Options) (*Watcher, error) {
	if opts.Client == nil {
		return nil, errors.New("missing API client")
	}

	if opts.Service == "" {
		opts.Service = DefaultService
	}

	return &Watcher{
		opts: opts,
		now:  time.Now,
	}, nil
}

func (w *Watcher) Ingest(ctx context.Context, out chan<- model.Observation) error {
	paths := []string{"/api/v1/events"}
	if len(w.opts.Namespaces) > 0 {
		paths = paths[:0]
		for _, namespace := range w.opts.Namespaces {
			paths = append(paths, "/api/v1/namespaces/"+namespace+"/events")
		}
	}

	g, gctx := errgroup.WithContext(ctx)

	for _, path := range paths {
		g.Go(func() error {
			return w.opts.Client.ListWatch(gctx, path, nil,
				// The initial list is only a watch cursor: past events are
				// not replayed.
				nil,
				func(raw *api.WatchEvent) error {
					w.handle(gctx, raw, out)
					return nil
				})
		})
	}

	return errors.WithStack(g.Wait())
}

// event is the subset of a core/v1 Event the watcher uses. MODIFIED
// frames carry occurrence repetitions (count increments), which matter
// to frequency detection as much as first occurrences.
type event struct {
	Metadata       api.ObjectMeta `json:"metadata"`
	Type           string         `json:"type"`
	Reason         string         `json:"reason"`
	Message        string         `json:"message"`
	LastTimestamp  string         `json:"lastTimestamp"`
	EventTime      string         `json:"eventTime"`
	InvolvedObject struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"involvedObject"`
}

func (w *Watcher) handle(ctx context.Context, raw *api.WatchEvent, out chan<- model.Observation) {
	if raw.Type != api.Added && raw.Type != api.Modified {
		return
	}

	evt := event{}
	if err := json.Unmarshal(raw.Object, &evt); err != nil {
		slog.WarnContext(ctx, "malformed kubernetes event", slog.Any("error", err))
		return
	}

	timestamp := w.timestamp(&evt)

	level := "info"
	if evt.Type == "Warning" {
		level = "warn"
	}

	message := evt.InvolvedObject.Kind + "/" + evt.InvolvedObject.Name + " " + evt.Reason + ": " + evt.Message

	envelope, err := json.Marshal(map[string]string{
		"time":  timestamp.Format(time.RFC3339Nano),
		"level": level,
		"msg":   message,
	})
	if err != nil {
		return
	}

	environment := w.opts.Environment
	if environment == "" {
		environment = w.namespace(&evt)
	}

	now := w.now()

	obs := model.Observation{
		ID:          model.NewID(),
		Service:     w.opts.Service,
		Environment: environment,
		Modality:    model.ModalityLog,
		Timestamp:   timestamp,
		IngestedAt:  now,
		Attributes: map[string]string{
			AttrNamespace: w.namespace(&evt),
			AttrKind:      evt.InvolvedObject.Kind,
			AttrName:      evt.InvolvedObject.Name,
			AttrReason:    evt.Reason,
		},
		Log: &model.LogRecord{Raw: string(envelope)},
	}

	select {
	case out <- obs:
	case <-ctx.Done():
	}
}

// timestamp mirrors the kubectl tutorial's fallback chain: legacy
// events carry lastTimestamp, events.k8s.io ones eventTime, and both
// always have a creation timestamp.
func (w *Watcher) timestamp(evt *event) time.Time {
	for _, value := range []string{evt.LastTimestamp, evt.EventTime, evt.Metadata.CreationTimestamp} {
		if value == "" {
			continue
		}

		if timestamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return timestamp
		}
	}

	return w.now()
}

func (w *Watcher) namespace(evt *event) string {
	if evt.Metadata.Namespace != "" {
		return evt.Metadata.Namespace
	}

	return evt.InvolvedObject.Namespace
}
