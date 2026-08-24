package processor

import (
	"context"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func parseLine(t *testing.T, line string) *model.Observation {
	t.Helper()

	obs := &model.Observation{
		Service:  "api",
		Modality: model.ModalityLog,
		Log:      &model.LogRecord{Raw: line},
	}

	next, err := NewParseLog().Process(context.Background(), obs, nil)
	if err != nil || !next {
		t.Fatalf("unexpected result: next=%v err=%+v", next, err)
	}

	return obs
}

func TestParseLogJSON(t *testing.T) {
	obs := parseLine(t, `{"time":"2026-08-24T14:02:00Z","level":"ERROR","msg":"database connection timeout","logger":"pool"}`)

	if obs.Log.Message != "database connection timeout" {
		t.Errorf("unexpected message: %q", obs.Log.Message)
	}

	if obs.Log.Level != "error" {
		t.Errorf("unexpected level: %q", obs.Log.Level)
	}

	want := time.Date(2026, 8, 24, 14, 2, 0, 0, time.UTC)
	if !obs.Timestamp.Equal(want) {
		t.Errorf("unexpected timestamp: %v", obs.Timestamp)
	}

	if obs.Log.EffectiveMessage() != "database connection timeout" {
		t.Errorf("unexpected effective message: %q", obs.Log.EffectiveMessage())
	}
}

func TestParseLogJSONEpochAndAliases(t *testing.T) {
	obs := parseLine(t, `{"ts":1787200920.5,"severity":"warning","message":"slow query detected"}`)

	if obs.Log.Message != "slow query detected" || obs.Log.Level != "warn" {
		t.Errorf("unexpected parse result: %+v", obs.Log)
	}

	if obs.Timestamp.Unix() != 1787200920 {
		t.Errorf("unexpected timestamp: %v", obs.Timestamp)
	}
}

func TestParseLogJournald(t *testing.T) {
	obs := parseLine(t, `{"MESSAGE":"Failed to start unit","PRIORITY":"3","__REALTIME_TIMESTAMP":"1787200920000000","_SYSTEMD_UNIT":"app.service"}`)

	if obs.Log.Message != "Failed to start unit" {
		t.Errorf("unexpected message: %q", obs.Log.Message)
	}

	if obs.Log.Level != "error" {
		t.Errorf("unexpected level: %q", obs.Log.Level)
	}

	if obs.Timestamp.Unix() != 1787200920 {
		t.Errorf("unexpected timestamp: %v", obs.Timestamp)
	}
}

func TestParseLogDockerTimestampPrefix(t *testing.T) {
	obs := parseLine(t, "2026-08-24T14:02:00.123456789Z payment failed: timeout")

	if obs.Log.Message != "payment failed: timeout" {
		t.Errorf("unexpected message: %q", obs.Log.Message)
	}

	if obs.Timestamp.IsZero() || obs.Timestamp.Year() != 2026 {
		t.Errorf("unexpected timestamp: %v", obs.Timestamp)
	}
}

func TestParseLogPlainTextFallback(t *testing.T) {
	obs := parseLine(t, "plain message without any timestamp")

	if obs.Log.Message != "" {
		t.Errorf("expected raw line to stay the message, got %q", obs.Log.Message)
	}

	if !obs.Timestamp.IsZero() {
		t.Errorf("expected no timestamp, got %v", obs.Timestamp)
	}

	if obs.Log.EffectiveMessage() != "plain message without any timestamp" {
		t.Errorf("unexpected effective message: %q", obs.Log.EffectiveMessage())
	}
}

func TestParseLogMalformedJSON(t *testing.T) {
	obs := parseLine(t, `{"broken json`)

	if obs.Log.Message != "" || obs.Log.Level != "" {
		t.Errorf("expected malformed json to be left as raw, got %+v", obs.Log)
	}
}
