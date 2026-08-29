package incident

import (
	"strings"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

// scenario reproduces the canonical use case: a deployment at 14:02, a
// log anomaly on checkout at 14:05, a metric anomaly on payment-api at
// 14:07 — one incident, two services, one correlated change. A
// separate anomaly the day before must stay a separate incident.
func scenario() []model.Event {
	base := time.Date(2026, 8, 24, 14, 5, 0, 0, time.UTC)

	return []model.Event{
		{
			ID: "old", Kind: "anomaly.log.rare_template", Source: "prod/blog", Service: "blog",
			Severity: model.SeverityWarning, Confidence: 0.5, Summary: "rare log template: disk almost full",
			Timestamp: base.Add(-24 * time.Hour),
			Signals: []model.Signal{
				{Kind: "log.rare_template", Source: "prod/blog", Score: 0.5, Summary: "rare log template: disk almost full", Timestamp: base.Add(-24 * time.Hour)},
			},
		},
		{
			ID: "trigger", Kind: "anomaly.correlated", Source: "prod/checkout", Service: "checkout",
			Severity: model.SeverityCritical, Confidence: 0.92,
			Summary:   "new log template after learning period: database connection timeout after <NUM>s (+1 correlated signals)",
			Timestamp: base,
			Signals: []model.Signal{
				{Kind: "log.new_template", Source: "prod/checkout", Score: 0.8, Summary: "new log template: database connection timeout after <NUM>s", Timestamp: base},
				{Kind: "log.new_template", Source: "prod/checkout", Score: 0.7, Summary: "new log template: database connection timeout after <NUM>s", Timestamp: base.Add(30 * time.Second)},
			},
			RelatedChanges: []model.RelatedChange{
				{Change: model.ChangeRecord{Type: "deployment", Version: "checkout:v1.8.2"}, OffsetSeconds: -180},
			},
		},
		{
			ID: "spread", Kind: "anomaly.metric.zscore", Source: "prod/payment-api", Service: "payment-api",
			Severity: model.SeverityWarning, Confidence: 0.8,
			Summary:   "pool_usage_percent = 97 deviates from baseline 40 (z = 5.1)",
			Timestamp: base.Add(2 * time.Minute),
			Signals: []model.Signal{
				{Kind: "metric.zscore", Source: "prod/payment-api", Score: 0.8, Summary: "pool_usage_percent = 97 deviates from baseline 40 (z = 5.1)", Timestamp: base.Add(2 * time.Minute)},
			},
			RelatedChanges: []model.RelatedChange{
				{Change: model.ChangeRecord{Type: "deployment", Version: "checkout:v1.8.2"}, OffsetSeconds: -300},
			},
		},
	}
}

func TestGroupSplitsOnGaps(t *testing.T) {
	incidents := Group(scenario(), 30*time.Minute)

	if len(incidents) != 2 {
		t.Fatalf("expected 2 incidents, got %d", len(incidents))
	}

	if incidents[0].Trigger.ID != "old" {
		t.Errorf("expected the day-before anomaly first, got %s", incidents[0].Trigger.ID)
	}

	if incidents[1].Trigger.ID != "trigger" || len(incidents[1].Events) != 2 {
		t.Errorf("expected the burst to hold trigger and spread, got %+v", incidents[1].Trigger.ID)
	}
}

func TestBuildAggregates(t *testing.T) {
	incidents := Group(scenario(), 30*time.Minute)
	burst := incidents[1]

	if burst.Severity != model.SeverityCritical || burst.Confidence != 0.92 {
		t.Errorf("expected the worst severity and confidence, got %s %.2f", burst.Severity, burst.Confidence)
	}

	if len(burst.Services) != 2 || burst.Services[0] != "checkout" || burst.Services[1] != "payment-api" {
		t.Errorf("expected services in order of appearance, got %v", burst.Services)
	}

	if !strings.Contains(burst.Title, "checkout: new log template") || !strings.Contains(burst.Title, "+1 service") {
		t.Errorf("unexpected title: %q", burst.Title)
	}

	// The two identical checkout signals fold into one evidence line.
	if len(burst.Evidence) != 2 {
		t.Fatalf("expected 2 evidence lines, got %+v", burst.Evidence)
	}

	if burst.Evidence[0].Kind != "log.new_template" || burst.Evidence[0].Count != 2 {
		t.Errorf("expected the trigger evidence folded x2, got %+v", burst.Evidence[0])
	}

	// The strongest occurrence speaks for the line.
	if burst.Evidence[0].MaxScore != 0.8 {
		t.Errorf("expected the max score kept, got %g", burst.Evidence[0].MaxScore)
	}

	// The same deployment reported by two events is one change, on the
	// incident clock: 3 minutes before the 14:05 trigger.
	if len(burst.Changes) != 1 {
		t.Fatalf("expected 1 deduplicated change, got %+v", burst.Changes)
	}

	if burst.Changes[0].BeforeStart != 3*time.Minute {
		t.Errorf("expected the change 3m before the trigger, got %s", burst.Changes[0].BeforeStart)
	}
}

func TestRenderReadsLikeABriefing(t *testing.T) {
	incidents := Group(scenario(), 30*time.Minute)

	var b strings.Builder
	Render(&b, incidents[1])
	text := b.String()

	for _, expected := range []string{
		"incident: checkout: new log template",
		"severity: critical (confidence 0.92)",
		"services: checkout, payment-api",
		"correlation, not causation",
		"deployment checkout:v1.8.2",
		"3m0s before the trigger",
		"trigger:",
		"evidence:",
		"x2",
		"pool_usage_percent",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("expected the briefing to contain %q, got:\n%s", expected, text)
		}
	}
}
