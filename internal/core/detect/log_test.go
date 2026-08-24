package detect

import (
	"fmt"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func logObservation(source string, templateID string, template string, changeType string, timestamp time.Time) *model.Observation {
	return &model.Observation{
		ID:        model.NewID(),
		Source:    source,
		Modality:  model.ModalityLog,
		Timestamp: timestamp,
		Attributes: map[string]string{
			model.AttrTemplateChangeType: changeType,
		},
		Log: &model.LogRecord{
			Raw:        template,
			TemplateID: templateID,
			Template:   template,
		},
	}
}

func hasSignal(signals []model.Signal, kind string) bool {
	for _, signal := range signals {
		if signal.Kind == kind {
			return true
		}
	}

	return false
}

func TestLogDetectorNewTemplate(t *testing.T) {
	config := DefaultLogConfig()
	config.LearningPeriod = time.Minute

	detector := NewLogDetector(config)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	// During the learning period new templates are normal.
	signals := detector.Detect(logObservation("api", "1", "startup complete", "cluster_created", start))
	if hasSignal(signals, SignalLogNewTemplate) {
		t.Fatal("expected no signal during learning period")
	}

	// After the learning period a new template must be signaled.
	signals = detector.Detect(logObservation("api", "2", "disk failure on <*>", "cluster_created", start.Add(2*time.Minute)))
	if !hasSignal(signals, SignalLogNewTemplate) {
		t.Fatalf("expected %s signal, got %+v", SignalLogNewTemplate, signals)
	}
}

func TestLogDetectorRareTemplate(t *testing.T) {
	config := DefaultLogConfig()
	config.LearningPeriod = 0
	config.RareMinObservations = 50
	config.RareThreshold = 2

	detector := NewLogDetector(config)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	for i := range 100 {
		detector.Detect(logObservation("api", "1", "request handled", "none", start.Add(time.Duration(i)*time.Second)))
	}

	// First occurrence is a new template (already covered); the second
	// occurrence of a still-rare template must be signaled as rare.
	detector.Detect(logObservation("api", "2", "strange failure", "cluster_created", start.Add(101*time.Second)))
	signals := detector.Detect(logObservation("api", "2", "strange failure", "none", start.Add(102*time.Second)))

	if !hasSignal(signals, SignalLogRareTemplate) {
		t.Fatalf("expected %s signal, got %+v", SignalLogRareTemplate, signals)
	}
}

func TestLogDetectorFrequencySpike(t *testing.T) {
	config := DefaultLogConfig()
	config.LearningPeriod = 0
	config.SpikeBucket = time.Minute
	config.SpikeFactor = 3
	config.SpikeMinCount = 10

	detector := NewLogDetector(config)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	// Two quiet buckets to learn the baseline (~2/minute).
	for bucket := range 2 {
		for i := range 2 {
			timestamp := start.Add(time.Duration(bucket)*time.Minute + time.Duration(i*20)*time.Second)
			detector.Detect(logObservation("api", "1", "timeout calling backend", "none", timestamp))
		}
	}

	// Burst in the third bucket.
	spiked := false
	for i := range 30 {
		timestamp := start.Add(2*time.Minute + time.Duration(i)*time.Second)
		signals := detector.Detect(logObservation("api", "1", "timeout calling backend", "none", timestamp))
		if hasSignal(signals, SignalLogFrequencySpike) {
			spiked = true
			break
		}
	}

	if !spiked {
		t.Fatal("expected a frequency spike signal during the burst")
	}
}

func TestLogDetectorMissingTemplate(t *testing.T) {
	config := DefaultLogConfig()
	config.LearningPeriod = 0
	config.DisappearanceMinCount = 5
	config.DisappearanceFactor = 3
	config.DisappearanceScanInterval = 10 * time.Second

	detector := NewLogDetector(config)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	// A heartbeat every 5 seconds.
	timestamp := start
	for i := range 20 {
		timestamp = start.Add(time.Duration(i*5) * time.Second)
		detector.Detect(logObservation("api", "1", "heartbeat ok", "none", timestamp))
	}

	// Other logs keep flowing but the heartbeat stops.
	var missing []model.Signal
	for i := range 30 {
		other := logObservation("api", "2", "request handled", "none", timestamp.Add(time.Duration((i+1)*5)*time.Second))
		signals := detector.Detect(other)
		if hasSignal(signals, SignalLogMissingTemplate) {
			missing = signals
			break
		}
	}

	if missing == nil {
		t.Fatal("expected a missing template signal after the heartbeat stopped")
	}
}

func TestLogDetectorMarkings(t *testing.T) {
	config := DefaultLogConfig()
	config.LearningPeriod = 0
	config.Markings = map[string]Marking{
		"noisy but fine":    MarkingIgnore,
		"panic during boot": MarkingSymptomatic,
	}

	detector := NewLogDetector(config)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	signals := detector.Detect(logObservation("api", "1", "noisy but fine", "cluster_created", start))
	if len(signals) != 0 {
		t.Fatalf("expected ignored template to produce no signal, got %+v", signals)
	}

	signals = detector.Detect(logObservation("api", "2", "panic during boot", "cluster_created", start.Add(time.Second)))
	if !hasSignal(signals, SignalLogSymptomatic) {
		t.Fatalf("expected %s signal, got %+v", SignalLogSymptomatic, signals)
	}
}

func TestLogDetectorMarkingsPersistence(t *testing.T) {
	config := DefaultLogConfig()
	config.LearningPeriod = 0
	config.Markings = map[string]Marking{"from config": MarkingNormal}

	detector := NewLogDetector(config)

	if err := detector.SetMarking("marked at runtime", MarkingIgnore); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	snapshot, err := detector.Snapshot()
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	restored := NewLogDetector(config)
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	markings := restored.Markings()
	if markings["marked at runtime"] != MarkingIgnore || markings["from config"] != MarkingNormal {
		t.Fatalf("unexpected restored markings: %+v", markings)
	}

	// Legacy snapshots (sources map alone) must still restore.
	legacy := []byte(`{"api": {"first_seen": "2026-08-24T12:00:00Z", "total": 10, "templates": {}}}`)

	fromLegacy := NewLogDetector(config)
	if err := fromLegacy.Restore(legacy); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	fromLegacy.mu.Lock()
	defer fromLegacy.mu.Unlock()

	if state := fromLegacy.sources["api"]; state == nil || state.Total != 10 {
		t.Fatalf("unexpected legacy restore: %+v", fromLegacy.sources)
	}
}

func TestLogDetectorSnapshotRoundTrip(t *testing.T) {
	config := DefaultLogConfig()
	config.LearningPeriod = 0

	detector := NewLogDetector(config)

	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	for i := range 50 {
		detector.Detect(logObservation("api", "1", "request handled", "none", start.Add(time.Duration(i)*time.Second)))
	}

	snapshot, err := detector.Snapshot()
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	restored := NewLogDetector(config)
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	restored.mu.Lock()
	defer restored.mu.Unlock()

	state := restored.sources["api"]
	if state == nil || state.Total != 50 || state.Templates["1"].Count != 50 {
		t.Fatalf("unexpected restored state: %+v", state)
	}
}

func BenchmarkLogDetector(b *testing.B) {
	config := DefaultLogConfig()
	config.LearningPeriod = 0

	detector := NewLogDetector(config)
	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	observations := make([]*model.Observation, 100)
	for i := range observations {
		observations[i] = logObservation("api", fmt.Sprintf("%d", i%10), "some template", "none", start.Add(time.Duration(i)*time.Second))
	}

	b.ResetTimer()

	for i := 0; b.Loop(); i++ {
		detector.Detect(observations[i%len(observations)])
	}
}
