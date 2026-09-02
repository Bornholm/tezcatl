package detect

import (
	"encoding/json"
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

// TestLogDetectorEvictsStaleTemplates mirrors the metric series cap:
// template churn (identifiers escaping the masking) mints statistics
// that are written once and never fed again, and nothing used to
// retire them.
func TestLogDetectorEvictsStaleTemplates(t *testing.T) {
	config := DefaultLogConfig()
	config.MaxTemplates = 3

	detector := NewLogDetector(config)

	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	// One template fed continuously must survive the churn.
	for i := range 30 {
		detector.Detect(logObservation("prod/api", "t-live", "GET <*> 200", "", start.Add(time.Duration(i)*time.Minute)))
	}

	// Churning templates, each seen once.
	for i := range 30 {
		id := fmt.Sprintf("t-churn-%d", i)
		detector.Detect(logObservation("prod/api", id, "session "+id, "", start.Add(time.Duration(i)*time.Minute)))
	}

	snapshot, err := detector.Snapshot()
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	var state struct {
		Sources map[string]struct {
			Templates map[string]json.RawMessage `json:"templates"`
		} `json:"sources"`
	}

	if err := json.Unmarshal(snapshot, &state); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	templates := state.Sources["prod/api"].Templates

	if len(templates) > 3 {
		t.Fatalf("expected at most 3 template stats, got %d", len(templates))
	}

	if _, exists := templates["t-live"]; !exists {
		t.Errorf("expected the continuously fed template to survive, got %v", templates)
	}
}

// TestLogDetectorBurstyTemplateIsNotMissing guards the regularity gate:
// a template arriving in bursts is silent most of the time, so its mean
// interval predicts nothing and its silence is not news. This is the
// nginx access log of an idle blog, the loudest false positive of the
// dogfooding instance.
func TestLogDetectorBurstyTemplateIsNotMissing(t *testing.T) {
	burst := func(t *testing.T, maxCV float64) []model.Signal {
		t.Helper()

		config := DefaultLogConfig()
		config.LearningPeriod = 0
		config.DisappearanceMinCount = 5
		config.DisappearanceFactor = 3
		config.DisappearanceScanInterval = 10 * time.Second
		config.DisappearanceMaxCV = maxCV

		detector := NewLogDetector(config)

		start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

		// Four bursts of ten lines one second apart, five minutes of
		// silence between them: the same total, none of the regularity.
		timestamp := start
		for range 4 {
			for range 10 {
				detector.Detect(logObservation("blog", "1", "GET /index.html 200", "none", timestamp))
				timestamp = timestamp.Add(time.Second)
			}

			timestamp = timestamp.Add(5 * time.Minute)
		}

		// Other logs keep flowing, which is what drives the scan.
		for i := range 60 {
			other := logObservation("blog", "2", "request handled", "none", timestamp.Add(time.Duration((i+1)*10)*time.Second))
			if signals := detector.Detect(other); hasSignal(signals, SignalLogMissingTemplate) {
				return signals
			}
		}

		return nil
	}

	if signals := burst(t, DefaultDisappearanceMaxCV); signals != nil {
		t.Fatalf("expected no missing template signal for a bursty template, got %+v", signals)
	}

	// Without the gate the very same stream signals, so the burst is
	// what the gate rejects and not some other guard along the way.
	if signals := burst(t, 0); signals == nil {
		t.Fatal("expected the ungated detector to signal the bursty template, the test proves nothing otherwise")
	}
}

func TestIntervalCV(t *testing.T) {
	for _, test := range []struct {
		name         string
		stats        templateStats
		wantMeasured bool
		wantBelow    float64
		wantAtLeast  float64
	}{
		{
			name:         "metronome",
			stats:        templateStats{MeanIntervalS: 60, IntervalVarianceS2: 0.25, IntervalSamples: 20},
			wantMeasured: true,
			wantBelow:    DefaultDisappearanceMaxCV,
		},
		{
			name:         "bursty",
			stats:        templateStats{MeanIntervalS: 6, IntervalVarianceS2: 10000, IntervalSamples: 20},
			wantMeasured: true,
			wantAtLeast:  DefaultDisappearanceMaxCV,
		},
		{
			// A state file written before the variance was tracked
			// carries thousands of occurrences and no dispersion. Read
			// as a variance of zero it would make every old template a
			// metronome, which is the loudest possible answer.
			name:  "restored without variance",
			stats: templateStats{MeanIntervalS: 60, Count: 5000},
		},
		{
			name:  "too few intervals to tell",
			stats: templateStats{MeanIntervalS: 60, IntervalVarianceS2: 4, IntervalSamples: minIntervalSamples - 1},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cv, measured := test.stats.intervalCV()

			if measured != test.wantMeasured {
				t.Fatalf("expected measured to be %t, got %t", test.wantMeasured, measured)
			}

			if test.wantBelow > 0 && cv >= test.wantBelow {
				t.Errorf("expected a coefficient of variation below %g, got %g", test.wantBelow, cv)
			}

			if test.wantAtLeast > 0 && cv < test.wantAtLeast {
				t.Errorf("expected a coefficient of variation of at least %g, got %g", test.wantAtLeast, cv)
			}
		})
	}
}

// TestLogDetectorRestoredTemplateIsNotExpectedBack covers the upgrade
// the dogfooding instance went through: a state file from a version
// with no variance restores templates seen thousands of times whose
// regularity is entirely unknown. Expecting them back on a variance of
// zero reports every one of them as a metronome that stopped.
func TestLogDetectorRestoredTemplateIsNotExpectedBack(t *testing.T) {
	config := DefaultLogConfig()
	config.LearningPeriod = 0
	config.DisappearanceScanInterval = 10 * time.Second

	detector := NewLogDetector(config)

	start := time.Date(2026, 8, 31, 6, 0, 0, 0, time.UTC)

	snapshot := fmt.Sprintf(`{"sources":{"prod/blog":{"first_seen":%q,"total":5000,"templates":{
		"1":{"template":"GET <*> HTTP/<NUM>.<NUM>","count":5000,"last_seen":%q,"mean_interval_s":4800}
	}}}}`, start.Format(time.RFC3339), start.Format(time.RFC3339))

	if err := detector.Restore([]byte(snapshot)); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	// Long past the mean interval, with other logs driving the scan.
	for i := range 60 {
		other := logObservation("blog", "2", "request handled", "none", start.Add(time.Duration(i+1)*time.Minute))
		if signals := detector.Detect(other); hasSignal(signals, SignalLogMissingTemplate) {
			t.Fatalf("expected no missing template signal for a restored template of unknown regularity, got %+v", signals)
		}
	}
}

// TestTemplateLiterals checks the measure that tells a message from a
// format: the real templates from the dogfooding instance on one side,
// the mined access log on the other.
func TestTemplateLiterals(t *testing.T) {
	for template, want := range map[string]bool{
		// Degenerate: everything is a placeholder, so the shape
		// matches every HTTP request ever served.
		`<IP> - - <*> +<NUM>] <*> <*> HTTP/<NUM>.<NUM>" <NUM> <NUM>`: false,
		`<NUM>/<NUM>/<NUM> <NUM>:<NUM>:<NUM>`:                        false,
		`<*> <*> <*>`:                                                false,
		`<IP>`:                                                       false,

		// Real messages: whole words survive the masking.
		"Invalid user <*> from <IP> port <NUM>":        true,
		"HTTP server listening on <IP>:<NUM>":          true,
		"Received disconnect from <IP> port <NUM>":     true,
		"payment gateway refused handshake":            true,
		"INFO GET /api/cart <NUM> in <*>":              true,
		"Received SIGTERM. Shutting down HTTP server.": true,
	} {
		detector := NewLogDetector(&LogConfig{MinTemplateLiterals: DefaultMinTemplateLiterals})

		if got := detector.informative(template); got != want {
			t.Errorf("informative(%q) = %v, want %v (%d literals)",
				template, got, want, templateLiterals(template))
		}
	}
}

// TestDegenerateTemplatesStayQuietAboutTheirShape covers the top noise
// family on the instance: an access-log template spiking twenty-two
// times a day, saying only that traffic changed.
func TestDegenerateTemplatesStayQuietAboutTheirShape(t *testing.T) {
	const degenerate = `<IP> - - <*> +<NUM>] <*> <*> HTTP/<NUM>.<NUM>" <NUM> <NUM>`

	detector := NewLogDetector(&LogConfig{
		LearningPeriod:      time.Minute,
		SpikeBucket:         time.Minute,
		SpikeFactor:         3,
		SpikeMinCount:       5,
		RareThreshold:       2,
		RareMinObservations: 10,
		MinTemplateLiterals: DefaultMinTemplateLiterals,
	})

	start := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)

	// A quiet baseline past the learning period.
	for i := range 40 {
		detector.Detect(logObservation("blog", "t1", degenerate, "none", start.Add(time.Duration(i)*time.Minute)))
	}

	// Then a burst in one bucket.
	burst := start.Add(60 * time.Minute)
	for i := range 30 {
		signals := detector.Detect(logObservation("blog", "t1", degenerate, "none", burst.Add(time.Duration(i)*time.Second)))

		for _, signal := range signals {
			switch signal.Kind {
			case SignalLogFrequencySpike, SignalLogNewTemplate, SignalLogRareTemplate:
				t.Fatalf("a template with no literal content must not report its shape: %s", signal.Summary)
			}
		}
	}
}
