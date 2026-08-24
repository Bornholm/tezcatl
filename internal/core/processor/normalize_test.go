package processor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func TestNormalize(t *testing.T) {
	normalize := NewNormalize(WithMaxLogLength(16))

	t.Run("fills missing fields", func(t *testing.T) {
		obs := model.Observation{
			Source:   "api",
			Modality: model.ModalityLog,
			Log:      &model.LogRecord{Raw: "hello"},
		}

		next, err := normalize.Process(context.Background(), &obs, nil)
		if err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}

		if !next {
			t.Fatal("expected observation to be kept")
		}

		if obs.ID == "" || obs.Timestamp.IsZero() || obs.IngestedAt.IsZero() {
			t.Fatalf("expected id and timestamps to be filled, got %+v", obs)
		}
	})

	t.Run("truncates long log lines", func(t *testing.T) {
		obs := model.Observation{
			Source:   "api",
			Modality: model.ModalityLog,
			Log:      &model.LogRecord{Raw: strings.Repeat("x", 100)},
		}

		if _, err := normalize.Process(context.Background(), &obs, nil); err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}

		if len(obs.Log.Raw) != 16 {
			t.Fatalf("expected truncation to 16 bytes, got %d", len(obs.Log.Raw))
		}
	})

	t.Run("keeps provided timestamp", func(t *testing.T) {
		timestamp := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

		obs := model.Observation{
			Source:    "api",
			Modality:  model.ModalityMetric,
			Timestamp: timestamp,
			Metric:    &model.MetricSample{Name: "up", Value: 1},
		}

		if _, err := normalize.Process(context.Background(), &obs, nil); err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}

		if !obs.Timestamp.Equal(timestamp) {
			t.Fatalf("expected timestamp to be preserved, got %v", obs.Timestamp)
		}
	})

	t.Run("derives source from service and environment", func(t *testing.T) {
		obs := model.Observation{
			Service:  "checkout",
			Modality: model.ModalityLog,
			Log:      &model.LogRecord{Raw: "hello"},
		}

		if _, err := normalize.Process(context.Background(), &obs, nil); err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}

		if obs.Source != "default/checkout" || obs.Environment != model.DefaultEnvironment {
			t.Fatalf("unexpected identity: %+v", obs)
		}

		obs = model.Observation{
			Service:     "checkout",
			Environment: "prod",
			Modality:    model.ModalityChange,
			Change:      &model.ChangeRecord{Type: "deployment"},
		}

		if _, err := normalize.Process(context.Background(), &obs, nil); err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}

		if obs.Source != "prod/checkout" {
			t.Fatalf("unexpected source: %q", obs.Source)
		}
	})

	t.Run("rejects invalid observations", func(t *testing.T) {
		invalid := []model.Observation{
			{Modality: model.ModalityLog, Log: &model.LogRecord{Raw: "no source"}},
			{Source: "api", Modality: model.ModalityLog},
			{Source: "api", Modality: model.ModalityMetric},
			{Source: "api", Modality: model.ModalityMetric, Metric: &model.MetricSample{}},
			{Source: "api", Modality: model.ModalityChange},
			{Source: "api", Modality: model.ModalityChange, Change: &model.ChangeRecord{}},
			{Source: "api", Modality: "unknown"},
		}

		for _, obs := range invalid {
			next, err := normalize.Process(context.Background(), &obs, nil)
			if err == nil || next {
				t.Fatalf("expected observation %+v to be rejected", obs)
			}
		}
	})
}
