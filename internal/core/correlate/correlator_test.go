package correlate

import (
	"fmt"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func TestCorrelatorAggregatesSignalsWithinWindow(t *testing.T) {
	config := DefaultConfig()
	config.Window = 30 * time.Second
	config.ContextBefore = 3
	config.ContextAfter = 2

	correlator := NewCorrelator(config)

	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	correlator.now = func() time.Time { return now }

	// Some context before the anomaly.
	for i := range 5 {
		correlator.Observe(&model.Observation{
			ID:       fmt.Sprintf("before-%d", i),
			Source:   "api",
			Modality: model.ModalityLog,
		})
	}

	trigger := &model.Observation{ID: "trigger", Source: "api", Modality: model.ModalityLog}
	correlator.Observe(trigger)
	correlator.Add(trigger, []model.Signal{
		{
			Kind:       "log.new_template",
			Modality:   model.ModalityLog,
			Source:     "api",
			Timestamp:  now,
			Score:      0.8,
			Summary:    "new log template: disk failure",
			Attributes: map[string]string{"template_id": "42"},
		},
	})

	// A correlated metric anomaly shortly after.
	metricObs := &model.Observation{ID: "metric", Source: "api", Modality: model.ModalityMetric}
	correlator.Observe(metricObs)
	correlator.Add(metricObs, []model.Signal{
		{
			Kind:       "metric.zscore",
			Modality:   model.ModalityMetric,
			Source:     "api",
			Timestamp:  now.Add(2 * time.Second),
			Score:      0.7,
			Summary:    "latency_ms deviates from baseline",
			Attributes: map[string]string{"metric": "latency_ms"},
		},
	})

	// A duplicate of the first signal.
	correlator.Add(trigger, []model.Signal{
		{
			Kind:       "log.new_template",
			Modality:   model.ModalityLog,
			Source:     "api",
			Timestamp:  now.Add(3 * time.Second),
			Score:      0.8,
			Summary:    "new log template: disk failure",
			Attributes: map[string]string{"template_id": "42"},
		},
	})

	correlator.Observe(&model.Observation{ID: "after-0", Source: "api", Modality: model.ModalityLog})

	// Nothing must be emitted before the window expires.
	events := []model.Event{}
	collect := func(evt model.Event) { events = append(events, evt) }

	correlator.Flush(false, collect)
	if len(events) != 0 {
		t.Fatalf("expected no event before window expiry, got %d", len(events))
	}

	now = now.Add(31 * time.Second)
	correlator.Flush(false, collect)

	if len(events) != 1 {
		t.Fatalf("expected exactly one correlated event, got %d", len(events))
	}

	evt := events[0]

	if evt.Kind != "anomaly.correlated" {
		t.Errorf("expected kind anomaly.correlated, got %q", evt.Kind)
	}

	if len(evt.Signals) != 2 {
		t.Fatalf("expected 2 deduplicated signals, got %d", len(evt.Signals))
	}

	if evt.Signals[0].Kind != "log.new_template" {
		t.Errorf("expected dominant signal first, got %q", evt.Signals[0].Kind)
	}

	if evt.Signals[0].Attributes["occurrences"] != "2" {
		t.Errorf("expected 2 occurrences of the dominant signal, got %q", evt.Signals[0].Attributes["occurrences"])
	}

	if evt.Attributes["multimodal"] != "true" {
		t.Errorf("expected multimodal event, got %+v", evt.Attributes)
	}

	if evt.Confidence <= 0.8 {
		t.Errorf("expected correlation to increase confidence above 0.8, got %f", evt.Confidence)
	}

	if len(evt.Context.Before) != 3 {
		t.Fatalf("expected 3 before observations, got %d", len(evt.Context.Before))
	}

	// The triggering observation must be the most recent "before".
	if evt.Context.Before[2].ID != "trigger" {
		t.Errorf("expected trigger as last before observation, got %q", evt.Context.Before[2].ID)
	}

	if len(evt.Context.After) != 2 || evt.Context.After[0].ID != "metric" || evt.Context.After[1].ID != "after-0" {
		t.Fatalf("unexpected after context: %+v", evt.Context.After)
	}

	// Sources must be independent: no cross-source aggregation state left.
	correlator.Flush(true, collect)
	if len(events) != 1 {
		t.Fatalf("expected no further event, got %d", len(events))
	}
}

func TestCorrelatorForceFlush(t *testing.T) {
	correlator := NewCorrelator(nil)

	obs := &model.Observation{ID: "obs", Source: "api", Modality: model.ModalityLog}
	correlator.Observe(obs)
	correlator.Add(obs, []model.Signal{
		{Kind: "log.new_template", Modality: model.ModalityLog, Source: "api", Score: 0.8, Summary: "boom"},
	})

	events := []model.Event{}
	correlator.Flush(true, func(evt model.Event) { events = append(events, evt) })

	if len(events) != 1 {
		t.Fatalf("expected forced flush to emit the pending event, got %d", len(events))
	}

	if events[0].Kind != "anomaly.log.new_template" {
		t.Errorf("expected single-signal kind, got %q", events[0].Kind)
	}
}
