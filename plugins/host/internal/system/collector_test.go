//go:build linux

package system

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func writeProcfs(t *testing.T, dir string, stat string) {
	t.Helper()

	files := map[string]string{
		"stat":    stat,
		"meminfo": "MemTotal:       16000000 kB\nMemFree:         2000000 kB\nMemAvailable:    4000000 kB\n",
		"loadavg": "1.25 0.80 0.60 2/1234 5678\n",
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}
	}
}

func TestCollector(t *testing.T) {
	procfs := t.TempDir()

	// user nice system idle iowait irq softirq steal
	writeProcfs(t, procfs, "cpu  100 0 100 700 100 0 0 0\ncpu0 50 0 50 350 50 0 0 0\n")

	collector, err := NewCollector(&Options{
		Service:     "dokku-host",
		Environment: "production",
		DiskPaths:   []string{t.TempDir()},
		procfs:      procfs,
	})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	out := make(chan model.Observation, 32)
	ctx := context.Background()

	collect := func() map[string]model.MetricSample {
		collector.poll(ctx, out)

		samples := map[string]model.MetricSample{}
		for {
			select {
			case obs := <-out:
				if obs.Service != "dokku-host" || obs.Modality != model.ModalityMetric {
					t.Fatalf("unexpected observation identity: %+v", obs)
				}

				samples[obs.Metric.Name] = *obs.Metric
			default:
				return samples
			}
		}
	}

	first := collect()

	// The first poll has no CPU baseline yet.
	if _, exists := first[MetricCPUPercent]; exists {
		t.Error("expected no cpu sample on first poll")
	}

	if memory := first[MetricMemoryUsedPercent].Value; memory < 74 || memory > 76 {
		t.Errorf("expected ~75%% memory used, got %f", memory)
	}

	if load := first[MetricLoad1].Value; load != 1.25 {
		t.Errorf("expected load 1.25, got %f", load)
	}

	if _, exists := first[MetricDiskUsedPercent]; !exists {
		t.Error("expected a disk usage sample")
	}

	// Second poll: +100 busy, +100 idle → 50% cpu.
	writeProcfs(t, procfs, "cpu  150 0 150 780 120 0 0 0\n")

	second := collect()

	if cpu := second[MetricCPUPercent].Value; cpu < 49 || cpu > 51 {
		t.Errorf("expected ~50%% cpu, got %f", cpu)
	}
}

func TestCollectorStopsOnContextCancellation(t *testing.T) {
	procfs := t.TempDir()
	writeProcfs(t, procfs, "cpu  1 0 1 1 0 0 0 0\n")

	collector, err := NewCollector(&Options{Interval: time.Hour, procfs: procfs})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- collector.Ingest(ctx, make(chan model.Observation, 32))
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not stop on cancellation")
	}
}
