package setup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/adapter/stdio"
	"github.com/bornholm/tezcatl/internal/config"
	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/model"
)

// syncBuffer collects the JSONL output of the pipeline.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) Events(t *testing.T) []model.Event {
	t.Helper()

	b.mu.Lock()
	defer b.mu.Unlock()

	events := []model.Event{}

	scanner := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	for scanner.Scan() {
		var evt model.Event
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("malformed event line %q: %+v", scanner.Text(), err)
		}

		events = append(events, evt)
	}

	return events
}

func TestRuntimeEndToEnd(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")

	newConfig := func() *config.Config {
		cfg := config.Default()
		cfg.Logs.Detection.LearningPeriod = 0
		cfg.Correlation.Window = config.Duration(50 * time.Millisecond)
		cfg.Pipeline.FlushInterval = config.Duration(10 * time.Millisecond)
		cfg.State.Dir = stateDir

		return cfg
	}

	var corpus strings.Builder
	for i := range 300 {
		fmt.Fprintf(&corpus, "request %d handled in %d ms\n", 1000+i, i%50)
	}
	corpus.WriteString("FATAL disk failure on /dev/sda1\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	output := &syncBuffer{}

	runtime, err := NewRuntime(ctx, newConfig(), WithEventsOutput(output))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	ingester := stdio.NewLogIngester(strings.NewReader(corpus.String()), stdio.Identity{Service: "api"})

	if err := runtime.Run(ctx, ingester); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	events := output.Events(t)
	if len(events) == 0 {
		t.Fatal("expected at least one anomaly event")
	}

	found := false
	for _, evt := range events {
		for _, signal := range evt.Signals {
			if signal.Kind == "log.new_template" && strings.Contains(signal.Attributes["template"], "disk failure") {
				found = true

				if len(evt.Context.Before) == 0 {
					t.Error("expected before context to be attached")
				}
			}
		}
	}

	if !found {
		t.Fatalf("expected a new template signal for the failure line, got %+v", events)
	}

	// Restart: known templates must not trigger anomalies again.
	output2 := &syncBuffer{}

	runtime2, err := NewRuntime(ctx, newConfig(), WithEventsOutput(output2))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	replay := "request 4242 handled in 7 ms\nFATAL disk failure on /dev/sdb9\n"

	if err := runtime2.Run(ctx, stdio.NewLogIngester(strings.NewReader(replay), stdio.Identity{Service: "api"})); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	for _, evt := range output2.Events(t) {
		for _, signal := range evt.Signals {
			if signal.Kind == "log.new_template" {
				t.Fatalf("expected known templates to be restored, got new template signal: %+v", signal)
			}
		}
	}
}

func TestRuntimeMultimodalCorrelation(t *testing.T) {
	cfg := config.Default()
	cfg.Logs.Detection.LearningPeriod = 0
	cfg.Correlation.Window = config.Duration(100 * time.Millisecond)
	cfg.Pipeline.FlushInterval = config.Duration(10 * time.Millisecond)

	maxUsage := 90.0
	cfg.Metrics.Detection.Thresholds = []detect.ThresholdRule{
		{Metric: "pool_usage_percent", Max: &maxUsage},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	output := &syncBuffer{}

	runtime, err := NewRuntime(ctx, cfg, WithEventsOutput(output))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	logs := stdio.NewLogIngester(strings.NewReader("FATAL pool exhausted\n"), stdio.Identity{Service: "api"})
	metrics := stdio.NewMetricIngester(strings.NewReader("pool_usage_percent 97\n"), stdio.Identity{Service: "api"})

	if err := runtime.Run(ctx, logs, metrics); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	events := output.Events(t)
	if len(events) != 1 {
		t.Fatalf("expected a single correlated event, got %d", len(events))
	}
}
