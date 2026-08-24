package port

import (
	"context"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

// Ingester feeds observations into the engine until its source is
// exhausted or the context is canceled. It must not close the channel.
type Ingester interface {
	Ingest(ctx context.Context, out chan<- model.Observation) error
}

// EmitFunc publishes an event produced by a processor.
type EmitFunc func(evt model.Event)

// Processor is a pipeline stage. It may mutate the observation in place,
// emit events, or drop the observation by returning false. A processor is
// called sequentially for observations of a same partition but must be
// safe for concurrent use across partitions.
type Processor interface {
	Name() string
	Process(ctx context.Context, obs *model.Observation, emit EmitFunc) (bool, error)
}

// EventSink publishes events to a destination. A failing sink must not
// block the pipeline indefinitely.
type EventSink interface {
	Publish(ctx context.Context, events []model.Event) error
	Close() error
}

// Flusher is implemented by processors holding time-based state (e.g.
// correlation windows) that must be flushed even when no observation
// arrives. The engine calls Flush periodically; force is true on the
// final flush before shutdown.
type Flusher interface {
	Flush(ctx context.Context, force bool, emit EmitFunc)
}

// Snapshotter is implemented by components whose learned state must
// survive restarts (template miner, detector baselines).
type Snapshotter interface {
	SnapshotKey() string
	Snapshot() ([]byte, error)
	Restore(data []byte) error
}

var ErrStateNotFound = errors.New("state not found")

// StateStore persists opaque engine state (template miner snapshots,
// detector baselines) across restarts. Load returns ErrStateNotFound
// when no state exists for the given key.
type StateStore interface {
	Save(ctx context.Context, key string, data []byte) error
	Load(ctx context.Context, key string) ([]byte, error)
	Close() error
}
