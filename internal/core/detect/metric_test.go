package detect

import (
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func metricObservation(source string, name string, value float64, timestamp time.Time) *model.Observation {
	return &model.Observation{
		ID:        model.NewID(),
		Source:    source,
		Modality:  model.ModalityMetric,
		Timestamp: timestamp,
		Metric: &model.MetricSample{
			Name:  name,
			Value: value,
		},
	}
}

func TestMetricDetectorZScore(t *testing.T) {
	detector := NewMetricDetector(nil)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	// Stable baseline around 100 with a small oscillation.
	for i := range 100 {
		value := 100 + float64(i%5)
		signals := detector.Detect(metricObservation("api", "latency_ms", value, start.Add(time.Duration(i)*time.Second)))
		if hasSignal(signals, SignalMetricZScore) {
			t.Fatalf("unexpected z-score signal on stable baseline at sample %d", i)
		}
	}

	signals := detector.Detect(metricObservation("api", "latency_ms", 500, start.Add(200*time.Second)))
	if !hasSignal(signals, SignalMetricZScore) {
		t.Fatalf("expected %s signal for outlier, got %+v", SignalMetricZScore, signals)
	}
}

// TestMetricDetectorMinDelta reproduces the idle-container situation:
// a CPU series flat around 0.03% has a near-zero variance, so a sample
// at 0.15% scores a huge z — the significance floor must silence it,
// while a genuinely large move still signals.
func TestMetricDetectorMinDelta(t *testing.T) {
	detector := NewMetricDetector(nil)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	for i := range 100 {
		value := 0.02 + float64(i%3)*0.01
		detector.Detect(metricObservation("prod/app", "docker.cpu.percent", value, start.Add(time.Duration(i)*time.Second)))
	}

	// 0.15%: statistically extreme, operationally nothing (delta below
	// the *.percent floor of 1 point).
	signals := detector.Detect(metricObservation("prod/app", "docker.cpu.percent", 0.15, start.Add(200*time.Second)))
	if hasSignal(signals, SignalMetricZScore) {
		t.Fatalf("expected the significance floor to silence a 0.12 point move, got %+v", signals)
	}

	// 42%: above the floor, must still signal.
	signals = detector.Detect(metricObservation("prod/app", "docker.cpu.percent", 42, start.Add(210*time.Second)))
	if !hasSignal(signals, SignalMetricZScore) {
		t.Fatalf("expected a z-score signal for a real move, got %+v", signals)
	}

	// A metric matching no floor keeps the previous behavior.
	if floor := detector.config.minDelta("queue_depth"); floor != 0 {
		t.Errorf("expected no floor for queue_depth, got %g", floor)
	}

	// Exact entries win over globs.
	config := DefaultMetricConfig()
	config.MinDeltas["docker.cpu.percent"] = 5
	if floor := config.minDelta("docker.cpu.percent"); floor != 5 {
		t.Errorf("expected the exact entry to win, got %g", floor)
	}

	if floor := config.minDelta("system.load1"); floor != 0.5 {
		t.Errorf("expected the default load1 floor, got %g", floor)
	}
}

// TestMetricDetectorTrendDriftMinDelta silences drift on near-zero
// series: a fast EWMA at 0.09 vs a slow one at 0.03 is a 3x relative
// divergence but a 0.06 point move.
func TestMetricDetectorTrendDriftMinDelta(t *testing.T) {
	detector := NewMetricDetector(nil)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	for i := range 60 {
		detector.Detect(metricObservation("prod/app", "docker.cpu.percent", 0.03, start.Add(time.Duration(i)*time.Second)))
	}

	for i := range 60 {
		signals := detector.Detect(metricObservation("prod/app", "docker.cpu.percent", 0.09, start.Add(time.Duration(60+i)*time.Second)))
		if hasSignal(signals, SignalMetricTrendDrift) {
			t.Fatalf("expected the significance floor to silence a 0.06 point drift at sample %d", i)
		}
	}
}

func TestMetricDetectorThreshold(t *testing.T) {
	maxValue := 90.0

	config := DefaultMetricConfig()
	config.Thresholds = []ThresholdRule{
		{Metric: "pool_usage_percent", Max: &maxValue},
	}

	detector := NewMetricDetector(config)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	signals := detector.Detect(metricObservation("api", "pool_usage_percent", 50, start))
	if hasSignal(signals, SignalMetricThreshold) {
		t.Fatalf("unexpected threshold signal for in-range value: %+v", signals)
	}

	signals = detector.Detect(metricObservation("api", "pool_usage_percent", 97, start.Add(time.Second)))
	if !hasSignal(signals, SignalMetricThreshold) {
		t.Fatalf("expected %s signal, got %+v", SignalMetricThreshold, signals)
	}
}

func TestMetricDetectorTrendDrift(t *testing.T) {
	detector := NewMetricDetector(nil)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	for i := range 60 {
		detector.Detect(metricObservation("api", "queue_depth", 10, start.Add(time.Duration(i)*time.Second)))
	}

	// Sustained climb: the fast EWMA must diverge from the slow one.
	drifted := false
	for i := range 60 {
		value := 10 + float64(i)*2
		signals := detector.Detect(metricObservation("api", "queue_depth", value, start.Add(time.Duration(60+i)*time.Second)))
		if hasSignal(signals, SignalMetricTrendDrift) {
			drifted = true
			break
		}
	}

	if !drifted {
		t.Fatal("expected a trend drift signal during the sustained climb")
	}
}

func TestMetricDetectorSnapshotRoundTrip(t *testing.T) {
	detector := NewMetricDetector(nil)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	for i := range 50 {
		detector.Detect(metricObservation("api", "latency_ms", 100, start.Add(time.Duration(i)*time.Second)))
	}

	snapshot, err := detector.Snapshot()
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	restored := NewMetricDetector(nil)
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	restored.mu.Lock()
	defer restored.mu.Unlock()

	stats := restored.series["api/latency_ms"]
	if stats == nil || stats.Count != 50 {
		t.Fatalf("unexpected restored state: %+v", stats)
	}
}
