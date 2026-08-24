package state

import (
	"context"
	"log/slog"
	"time"

	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/pkg/errors"
)

const DefaultSaveInterval = 30 * time.Second

// Manager restores the learned state of the registered snapshotters at
// startup and saves it periodically, plus one final time at shutdown.
type Manager struct {
	store        port.StateStore
	snapshotters []port.Snapshotter
	interval     time.Duration
}

func NewManager(store port.StateStore, interval time.Duration, snapshotters ...port.Snapshotter) *Manager {
	if interval <= 0 {
		interval = DefaultSaveInterval
	}

	return &Manager{
		store:        store,
		snapshotters: snapshotters,
		interval:     interval,
	}
}

// RestoreAll loads the persisted state of every snapshotter. A missing
// state is not an error: the component simply starts fresh.
func (m *Manager) RestoreAll(ctx context.Context) error {
	for _, snapshotter := range m.snapshotters {
		data, err := m.store.Load(ctx, snapshotter.SnapshotKey())
		if err != nil {
			if errors.Is(err, port.ErrStateNotFound) {
				continue
			}

			return errors.Wrapf(err, "could not load state %q", snapshotter.SnapshotKey())
		}

		if err := snapshotter.Restore(data); err != nil {
			return errors.Wrapf(err, "could not restore state %q", snapshotter.SnapshotKey())
		}

		slog.InfoContext(ctx, "state restored", slog.String("key", snapshotter.SnapshotKey()))
	}

	return nil
}

// SaveAll snapshots every snapshotter into the store.
func (m *Manager) SaveAll(ctx context.Context) error {
	for _, snapshotter := range m.snapshotters {
		data, err := snapshotter.Snapshot()
		if err != nil {
			return errors.Wrapf(err, "could not snapshot %q", snapshotter.SnapshotKey())
		}

		if err := m.store.Save(ctx, snapshotter.SnapshotKey(), data); err != nil {
			return errors.Wrapf(err, "could not save state %q", snapshotter.SnapshotKey())
		}
	}

	return nil
}

// Run saves periodically until the context is canceled, then saves one
// final time so no learning is lost at shutdown.
func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := m.SaveAll(ctx); err != nil {
				slog.ErrorContext(ctx, "could not save state", slog.Any("error", err))
			}

		case <-ctx.Done():
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()

			if err := m.SaveAll(finalCtx); err != nil {
				return errors.WithStack(err)
			}

			return nil
		}
	}
}
