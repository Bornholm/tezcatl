package detect

import (
	"fmt"
	"strings"
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
	// the *percent floor of 1 point).
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

	// The defaults floor percentages, whose scale is known from the
	// unit alone, and nothing else: a metric name belongs to whoever
	// emits it, so the core does not ship floors for names a plugin
	// happens to use today.
	if floor := DefaultMetricConfig().minDelta("system.load1"); floor != 0 {
		t.Errorf("expected no default floor for a plugin's own metric name, got %g", floor)
	}
}

// TestDefaultMinDeltasCoverPercentages guards the glob that the
// dogfooding instance caught out: path.Match gives "." no special
// meaning, so a "*.percent" pattern silently misses every metric whose
// suffix is "_percent", and those kept firing on 0.07 point moves.
func TestDefaultMinDeltasCoverPercentages(t *testing.T) {
	config := &MetricConfig{MinDeltas: DefaultMinDeltas()}

	// Every percentage the host plugin emits.
	metrics := []string{
		"system.cpu.percent",
		"docker.cpu.percent",
		"docker.memory.used_percent",
		"system.disk.used_percent",
		"system.memory.used_percent",
	}

	for _, metric := range metrics {
		if floor := config.minDelta(metric); floor != 1 {
			t.Errorf("expected a 1 point floor for %s, got %v", metric, floor)
		}
	}

	if floor := config.minDelta("http.requests.total"); floor != 0 {
		t.Errorf("expected no floor for a non-percentage metric, got %v", floor)
	}
}

// TestMetricDetectorMinDeltaUnderscorePercent replays an event the
// instance produced: container memory at 0.44% moving to 0.51%,
// reported critical at z = 5.9.
func TestMetricDetectorMinDeltaUnderscorePercent(t *testing.T) {
	detector := NewMetricDetector(nil)

	start := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)

	for i := range 100 {
		value := 0.44 + float64(i%3)*0.01
		detector.Detect(metricObservation("production/offen", "docker.memory.used_percent", value, start.Add(time.Duration(i)*time.Second)))
	}

	signals := detector.Detect(metricObservation("production/offen", "docker.memory.used_percent", 0.508, start.Add(200*time.Second)))
	if hasSignal(signals, SignalMetricZScore) {
		t.Fatalf("expected the floor to silence a 0.07 point move, got %+v", signals)
	}

	// A move that actually matters still gets through.
	signals = detector.Detect(metricObservation("production/offen", "docker.memory.used_percent", 87, start.Add(260*time.Second)))
	if !hasSignal(signals, SignalMetricZScore) {
		t.Fatalf("expected memory at 87%% to signal, got %+v", signals)
	}
}

// TestMetricDetectorEvictsStaleSeries covers the cardinality leak the
// dogfooding instance showed: environments mint series keys that are
// written once and never fed again (a deploy container, a one-off job,
// a pod name), and nothing used to retire them.
func TestMetricDetectorEvictsStaleSeries(t *testing.T) {
	detector := NewMetricDetector(&MetricConfig{
		WarmupSamples: 30,
		Alpha:         0.05,
		ZThreshold:    3,
		MaxSeries:     3,
	})

	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	// A series that keeps receiving samples must survive the churn.
	for i := range 20 {
		detector.Detect(metricObservation("prod/app", "cpu.percent", 10, start.Add(time.Duration(i)*time.Minute)))
	}

	// Ephemeral keys, each seen exactly once.
	for i := range 20 {
		obs := metricObservation("prod/app", "cpu.percent", 5, start.Add(time.Duration(i)*time.Minute))
		obs.Metric.Labels = map[string]string{"container": fmt.Sprintf("app.run.%d", i)}
		detector.Detect(obs)
	}

	series := detector.Series()
	if len(series) > 3 {
		t.Fatalf("expected at most 3 series, got %d", len(series))
	}

	var kept bool
	for _, info := range series {
		if !strings.Contains(info.Key, "container=") {
			kept = true
		}
	}

	if !kept {
		t.Errorf("expected the continuously fed series to survive, got %+v", series)
	}
}

// TestMetricDetectorUncappedByDefault documents that 0 means unlimited.
func TestMetricDetectorNoCap(t *testing.T) {
	detector := NewMetricDetector(&MetricConfig{WarmupSamples: 30, Alpha: 0.05, ZThreshold: 3, MaxSeries: 0})

	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	for i := range 50 {
		obs := metricObservation("prod/app", "cpu.percent", 5, start)
		obs.Metric.Labels = map[string]string{"container": fmt.Sprintf("app.run.%d", i)}
		detector.Detect(obs)
	}

	if got := len(detector.Series()); got != 50 {
		t.Errorf("expected no cap to keep all 50 series, got %d", got)
	}
}

// TestMetricDetectorEvictsRestoredOverflow covers lowering the cap
// below the size of an already persisted state.
func TestMetricDetectorEvictsRestoredOverflow(t *testing.T) {
	wide := NewMetricDetector(&MetricConfig{WarmupSamples: 30, Alpha: 0.05, ZThreshold: 3, MaxSeries: 0})

	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	for i := range 20 {
		obs := metricObservation("prod/app", "cpu.percent", 5, start.Add(time.Duration(i)*time.Minute))
		obs.Metric.Labels = map[string]string{"container": fmt.Sprintf("app.run.%d", i)}
		wide.Detect(obs)
	}

	snapshot, err := wide.Snapshot()
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	narrow := NewMetricDetector(&MetricConfig{WarmupSamples: 30, Alpha: 0.05, ZThreshold: 3, MaxSeries: 5})
	if err := narrow.Restore(snapshot); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	// The next new series brings the state back under the cap.
	narrow.Detect(metricObservation("prod/app", "memory.percent", 5, start.Add(time.Hour)))

	if got := len(narrow.Series()); got > 5 {
		t.Errorf("expected the restored state to be trimmed to 5 series, got %d", got)
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
