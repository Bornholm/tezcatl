package plugin

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

// buildMockPlugin compiles the mock source plugin into a temporary
// plugins directory.
func buildMockPlugin(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, Prefix+"mock"), "./testdata/mock")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("could not build mock plugin: %v\n%s", err, output)
	}

	return dir
}

func TestSourcePluginRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin binary")
	}

	dir := buildMockPlugin(t)

	plugins, err := Discover(dir)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	path, exists := plugins["mock"]
	if !exists {
		t.Fatalf("expected mock plugin to be discovered, got %+v", plugins)
	}

	ingester := NewSourceIngester("mock", path, []byte(`{"count": 5, "service": "checkout"}`))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out := make(chan model.Observation, 16)

	done := make(chan error, 1)
	go func() {
		done <- ingester.Ingest(ctx, out)
	}()

	observations := []model.Observation{}
	timeout := time.After(20 * time.Second)

	for len(observations) < 5 {
		select {
		case obs := <-out:
			observations = append(observations, obs)
		case <-timeout:
			t.Fatalf("expected 5 observations, got %d", len(observations))
		}
	}

	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ingester did not stop after cancellation")
	}

	first := observations[0]

	if first.ID != "mock-0" || first.Service != "checkout" || first.Modality != model.ModalityLog {
		t.Fatalf("mangled observation: %+v", first)
	}

	if first.Log == nil || first.Log.Raw != "mock line 0" {
		t.Fatalf("mangled log record: %+v", first.Log)
	}

	if first.IngestedAt.IsZero() {
		t.Fatal("expected ingestion timestamp to be stamped by the host")
	}
}

func TestLookupUnknownPlugin(t *testing.T) {
	if _, err := Lookup(t.TempDir(), "missing"); err == nil {
		t.Fatal("expected an error for a missing plugin")
	}
}
