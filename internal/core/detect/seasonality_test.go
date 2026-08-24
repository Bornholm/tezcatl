package detect

import (
	"testing"
	"time"
)

// TestSeasonalitySuppressesKnownBusyHourSpike: a template doing ~60/min
// at 09:00 daily must not be flagged as a spike at 09:00 just because
// the night was quiet, while the same burst at 03:00 must be flagged.
func TestSeasonalitySpikeBaselines(t *testing.T) {
	run := func(t *testing.T, seasonality string, burstHour int, wantSpike bool) {
		t.Helper()

		config := DefaultLogConfig()
		config.LearningPeriod = 0
		config.SpikeBucket = time.Minute
		config.SpikeFactor = 3
		config.SpikeMinCount = 10
		config.Seasonality = seasonality

		detector := NewLogDetector(config)

		// Three days of history: 60 logs/minute during hour 9, one lone
		// log at 22:00 keeping the series alive through the night.
		day := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
		for d := range 3 {
			for minute := range 10 {
				for second := range 60 {
					timestamp := day.AddDate(0, 0, d).Add(9*time.Hour + time.Duration(minute)*time.Minute + time.Duration(second)*time.Second)
					detector.Detect(logObservation("api", "1", "request handled", "none", timestamp))
				}
			}

			detector.Detect(logObservation("api", "1", "request handled", "none", day.AddDate(0, 0, d).Add(22*time.Hour)))
		}

		// Burst on day 4 at the given hour: 60 logs in one minute.
		spiked := false
		for second := range 60 {
			timestamp := day.AddDate(0, 0, 3).Add(time.Duration(burstHour)*time.Hour + time.Duration(second)*time.Second)
			signals := detector.Detect(logObservation("api", "1", "request handled", "none", timestamp))
			if hasSignal(signals, SignalLogFrequencySpike) {
				spiked = true
			}
		}

		if spiked != wantSpike {
			t.Fatalf("seasonality=%s burst at %02d:00: expected spike=%v, got %v", seasonality, burstHour, wantSpike, spiked)
		}
	}

	t.Run("busy hour burst is normal with hourly seasonality", func(t *testing.T) {
		run(t, SeasonalityHourly, 9, false)
	})

	t.Run("same burst at a quiet hour is a spike", func(t *testing.T) {
		run(t, SeasonalityHourly, 3, true)
	})

	t.Run("flat baseline flags the busy hour burst after a quiet night", func(t *testing.T) {
		run(t, SeasonalityNone, 9, true)
	})
}

// TestSeasonalityMissingTemplate: a nightly cron (02:00) must not be
// reported missing when checked at noon, but the regular heartbeat must
// still be reported when it stops.
func TestSeasonalityMissingTemplate(t *testing.T) {
	config := DefaultLogConfig()
	config.LearningPeriod = 0
	config.DisappearanceMinCount = 3
	config.DisappearanceFactor = 3
	config.DisappearanceScanInterval = 10 * time.Second
	config.Seasonality = SeasonalityHourly
	config.SeasonalMinObservations = 50

	detector := NewLogDetector(config)

	day := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)

	// Three days of history: nightly backup at 02:00, and steady noon
	// traffic making hour 12 a known-active hour.
	for d := range 3 {
		detector.Detect(logObservation("api", "backup", "backup completed", "none", day.AddDate(0, 0, d).Add(2*time.Hour)))
		detector.Detect(logObservation("api", "backup", "backup completed", "none", day.AddDate(0, 0, d).Add(2*time.Hour+time.Minute)))

		for i := range 60 {
			detector.Detect(logObservation("api", "traffic", "request handled", "none", day.AddDate(0, 0, d).Add(12*time.Hour+time.Duration(i)*time.Minute)))
		}
	}

	// Day 4 at noon: traffic flows, the backup template is silent since
	// 02:01 — expected, it never runs at noon.
	backupMissing := false
	for i := range 30 {
		timestamp := day.AddDate(0, 0, 3).Add(12*time.Hour + time.Duration(i)*time.Minute)
		signals := detector.Detect(logObservation("api", "traffic", "request handled", "none", timestamp))

		for _, signal := range signals {
			if signal.Kind == SignalLogMissingTemplate && signal.Attributes["template"] == "backup completed" {
				backupMissing = true
			}
		}
	}

	if backupMissing {
		t.Fatal("nightly backup template must not be reported missing at noon")
	}
}
