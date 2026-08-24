package fs

import (
	"context"
	"testing"

	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/pkg/errors"
)

func TestStateStore(t *testing.T) {
	store, err := NewStateStore(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	ctx := context.Background()

	if _, err := store.Load(ctx, "missing"); !errors.Is(err, port.ErrStateNotFound) {
		t.Fatalf("expected ErrStateNotFound, got %+v", err)
	}

	if err := store.Save(ctx, "drain/prod", []byte("state-1")); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if err := store.Save(ctx, "drain/prod", []byte("state-2")); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	data, err := store.Load(ctx, "drain/prod")
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if string(data) != "state-2" {
		t.Fatalf("expected latest state, got %q", data)
	}
}
