package grpc

import (
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

// TestLogRecordRoundTrip guards the wire contract a source relies on:
// what it parsed itself must survive the trip to the server, otherwise
// the server has to guess it back from the flattened line.
func TestLogRecordRoundTrip(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)

	original := &model.Observation{
		ID:        "obs-1",
		Source:    "production/api",
		Service:   "api",
		Modality:  model.ModalityLog,
		Timestamp: now,
		Log: &model.LogRecord{
			Raw:     `{"MESSAGE":"disk almost full","PRIORITY":"4"}`,
			Message: "disk almost full",
			Level:   "warn",
		},
	}

	restored := FromProtoObservation(ToProtoObservation(original), now)

	if restored.Log == nil {
		t.Fatal("expected a log record")
	}

	if restored.Log.Message != "disk almost full" {
		t.Errorf("expected the source message to survive, got %q", restored.Log.Message)
	}

	if restored.Log.Level != "warn" {
		t.Errorf("expected the source level to survive, got %q", restored.Log.Level)
	}

	if restored.Log.Raw != original.Log.Raw {
		t.Errorf("expected the raw line to survive, got %q", restored.Log.Raw)
	}
}

// TestLogRecordRoundTripEmpty keeps the common case cheap: a source
// that only has a line of text sends nothing more.
func TestLogRecordRoundTripEmpty(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)

	proto := ToProtoObservation(&model.Observation{
		Modality: model.ModalityLog,
		Log:      &model.LogRecord{Raw: "plain line"},
	})

	if proto.GetLog().GetMessage() != "" || proto.GetLog().GetLevel() != "" {
		t.Errorf("expected no extra fields on the wire, got %+v", proto.GetLog())
	}

	restored := FromProtoObservation(proto, now)
	if restored.Log.Message != "" || restored.Log.Level != "" {
		t.Errorf("expected an unparsed record, got %+v", restored.Log)
	}
}
