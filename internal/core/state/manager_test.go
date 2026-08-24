package state

import (
	"context"
	"testing"

	"github.com/bornholm/tezcatl/internal/adapter/fs"
)

type fakeSnapshotter struct {
	key   string
	state []byte
}

func (s *fakeSnapshotter) SnapshotKey() string {
	return s.key
}

func (s *fakeSnapshotter) Snapshot() ([]byte, error) {
	return s.state, nil
}

func (s *fakeSnapshotter) Restore(data []byte) error {
	s.state = data
	return nil
}

func TestManagerSaveAndRestore(t *testing.T) {
	store, err := fs.NewStateStore(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	ctx := context.Background()

	first := &fakeSnapshotter{key: "first", state: []byte("learned-1")}
	second := &fakeSnapshotter{key: "second", state: []byte("learned-2")}

	manager := NewManager(store, 0, first, second)

	// Restoring with no persisted state must be a no-op.
	if err := manager.RestoreAll(ctx); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if err := manager.SaveAll(ctx); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	restoredFirst := &fakeSnapshotter{key: "first"}
	restoredSecond := &fakeSnapshotter{key: "second"}

	restored := NewManager(store, 0, restoredFirst, restoredSecond)
	if err := restored.RestoreAll(ctx); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if string(restoredFirst.state) != "learned-1" || string(restoredSecond.state) != "learned-2" {
		t.Fatalf("unexpected restored state: %q %q", restoredFirst.state, restoredSecond.state)
	}
}
