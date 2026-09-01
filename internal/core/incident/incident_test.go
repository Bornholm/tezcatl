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
	incidents := Group(scenario(), Options{})

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
	incidents := Group(scenario(), Options{})
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
	incidents := Group(scenario(), Options{})

	var b strings.Builder
	Render(&b, incidents[1])
	text := b.String()

	for _, expected := range []string{
		"incident: checkout: new log template",
		"severity: critical (confidence 0.92)",
		"services: checkout, payment-api",
		"correlation, not causation",
		"deployment checkout:v1.8.2",
		"3m before the trigger",
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

// nightlyNoise reproduces what the dogfooding instance actually
// produces: unrelated services anomalizing every few minutes all night
// (a web server's access-log patterns, a host's CPU), interleaved with
// one real deployment burst. Pure time-proximity grouping chained all
// of it into a single 5-hour "incident"; relatedness must not.
func nightlyNoise() []model.Event {
	base := time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC)

	events := []model.Event{}

	// Background: blog and host take turns every ~8 minutes for 3 hours,
	// which is well inside any usable gap.
	for i := range 12 {
		at := base.Add(time.Duration(i) * 16 * time.Minute)

		events = append(events,
			model.Event{
				ID: "blog-" + string(rune('a'+i)), Kind: "anomaly.log.frequency_spike",
				Source: "production/blog", Service: "blog", Severity: model.SeverityWarning,
				Timestamp: at, Summary: "frequency spike for template",
				Signals: []model.Signal{{Kind: "log.frequency_spike", Source: "production/blog", Timestamp: at}},
			},
			model.Event{
				ID: "host-" + string(rune('a'+i)), Kind: "anomaly.metric.zscore",
				Source: "production/host", Service: "host", Severity: model.SeverityWarning,
				Timestamp: at.Add(8 * time.Minute), Summary: "system.load1 deviates from baseline",
				Signals: []model.Signal{{Kind: "metric.zscore", Source: "production/host", Timestamp: at.Add(8 * time.Minute)}},
			},
		)
	}

	// The real story: a deployment of automata, its CPU spiking, and
	// leash-toolbox reacting seconds later.
	deploy := base.Add(70 * time.Minute)
	change := []model.RelatedChange{{Change: model.ChangeRecord{Type: "deployment", Version: "automata:00c698e"}, OffsetSeconds: -60}}

	events = append(events,
		model.Event{
			ID: "deploy-cpu", Kind: "anomaly.metric.zscore", Source: "production/automata", Service: "automata",
			Severity: model.SeverityCritical, Confidence: 0.95, Timestamp: deploy,
			Summary:        "docker.cpu.percent = 8.5 deviates from baseline 0.08 (z = 234)",
			Signals:        []model.Signal{{Kind: "metric.zscore", Source: "production/automata", Score: 0.95, Timestamp: deploy}},
			RelatedChanges: change,
		},
		model.Event{
			ID: "deploy-spread", Kind: "anomaly.metric.zscore", Source: "production/leash-toolbox", Service: "leash-toolbox",
			Severity: model.SeverityWarning, Confidence: 0.8, Timestamp: deploy.Add(5 * time.Second),
			Summary: "docker.memory.used_percent deviates from baseline",
			Signals: []model.Signal{{Kind: "metric.zscore", Source: "production/leash-toolbox", Score: 0.8, Timestamp: deploy.Add(5 * time.Second)}},
		},
		model.Event{
			ID: "deploy-sigterm", Kind: "anomaly.log.missing_template", Source: "production/automata", Service: "automata",
			Severity: model.SeverityWarning, Timestamp: deploy.Add(3 * time.Minute),
			Summary:        "expected log template not seen: Received SIGTERM",
			Signals:        []model.Signal{{Kind: "log.missing_template", Source: "production/automata", Timestamp: deploy.Add(3 * time.Minute)}},
			RelatedChanges: change,
		},
	)

	return events
}

// TestGroupIsolatesRealBurstFromBackground is the regression the
// instance taught: the deployment must be its own story, not a
// paragraph inside a night-long one.
func TestGroupIsolatesRealBurstFromBackground(t *testing.T) {
	incidents := Group(nightlyNoise(), Options{})

	var deployIncident *Incident
	for i := range incidents {
		for _, event := range incidents[i].Events {
			if event.ID == "deploy-cpu" {
				deployIncident = &incidents[i]
			}
		}
	}

	if deployIncident == nil {
		t.Fatal("the deployment burst vanished")
	}

	// Exactly the three events of the deployment, nothing swept in.
	if len(deployIncident.Events) != 3 {
		t.Fatalf("expected the deployment burst alone, got %d events: %+v", len(deployIncident.Events), deployIncident.Services)
	}

	if len(deployIncident.Services) != 2 {
		t.Errorf("expected automata and leash-toolbox, got %v", deployIncident.Services)
	}

	if deployIncident.End.Sub(deployIncident.Start) > 5*time.Minute {
		t.Errorf("expected a short burst, got %s", deployIncident.End.Sub(deployIncident.Start))
	}

	// The background stays separate, and no incident swallows the night.
	for _, entry := range incidents {
		if entry.End.Sub(entry.Start) > time.Hour {
			t.Errorf("expected no incident longer than the cap, got %s (%s)", entry.End.Sub(entry.Start), entry.Title)
		}
	}
}

// TestGroupCapsChronicServices covers a service that never stops
// anomalizing: it must be reported as successive bounded incidents
// rather than one endless one.
func TestGroupCapsChronicServices(t *testing.T) {
	base := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)

	events := []model.Event{}
	for i := range 40 {
		at := base.Add(time.Duration(i) * 10 * time.Minute)
		events = append(events, model.Event{
			ID: "chronic", Kind: "anomaly.log.frequency_spike", Source: "production/blog", Service: "blog",
			Severity: model.SeverityWarning, Timestamp: at, Summary: "frequency spike",
			Signals: []model.Signal{{Kind: "log.frequency_spike", Source: "production/blog", Timestamp: at}},
		})
	}

	incidents := Group(events, Options{})

	if len(incidents) < 6 {
		t.Errorf("expected the chronic service split into bounded incidents, got %d", len(incidents))
	}

	for _, entry := range incidents {
		if entry.End.Sub(entry.Start) > time.Hour {
			t.Errorf("expected each incident under the cap, got %s", entry.End.Sub(entry.Start))
		}
	}
}

func TestRenderMarkdownExplainsItsSchema(t *testing.T) {
	incidents := Group(scenario(), Options{})

	var b strings.Builder
	RenderMarkdown(&b, incidents, Period{
		Since:     time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		Generated: time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC),
	})
	report := b.String()

	// The reader must learn what the data means before reading it.
	for _, expected := range []string{
		"# Tezcatl incident report",
		"## How to read this report",
		"Placeholders in templates",
		"`<NUM>`",
		"Do not read a placeholder as",
		"correlation, and only correlation",
		"### Signal kinds",
		"`log.frequency_spike`",
		"`metric.zscore`",
		"What tezcatl does not know",
		"It does not know causality",
		"A quiet detector is not a healthy system",
		"Incident boundaries are a heuristic",
	} {
		if !strings.Contains(report, expected) {
			t.Errorf("expected the report to explain %q", expected)
		}
	}

	// And then the incidents themselves.
	for _, expected := range []string{
		"## Incident 2 — checkout: new log template",
		"### Trigger",
		"### Changes near this incident (correlation only)",
		"3m before the trigger",
		"### Evidence",
		"| `log.new_template` |",
		"pool_usage_percent",
	} {
		if !strings.Contains(report, expected) {
			t.Errorf("expected the report to contain %q, got:\n%s", expected, report)
		}
	}

	// The schema is written once, not per incident.
	if count := strings.Count(report, "## How to read this report"); count != 1 {
		t.Errorf("expected the schema once, got %d times", count)
	}
}

// TestRenderMarkdownSurvivesLogLines guards the tables: real log lines
// carry pipes and newlines, which would otherwise end a cell or a row.
func TestRenderMarkdownSurvivesLogLines(t *testing.T) {
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	events := []model.Event{{
		ID: "nasty", Kind: "anomaly.log.new_template", Source: "prod/api", Service: "api",
		Severity: model.SeverityWarning, Timestamp: at,
		Summary: "new log template: a | b\nc",
		Signals: []model.Signal{
			{Kind: "log.new_template", Source: "prod/api", Timestamp: at, Summary: "col | umn\nnewline"},
		},
	}}

	var b strings.Builder
	RenderMarkdown(&b, Group(events, Options{}), Period{})
	report := b.String()

	evidence := report[strings.Index(report, "### Evidence"):]
	for _, line := range strings.Split(evidence, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}

		// Six columns means seven pipes; an unescaped one would add more.
		if got := strings.Count(line, "|") - strings.Count(line, "\\|"); got != 7 {
			t.Errorf("expected 7 unescaped pipes in an evidence row, got %d: %s", got, line)
		}
	}

	if strings.Contains(evidence, "newline\n") && !strings.Contains(evidence, "col \\| umn newline") {
		t.Error("expected newlines folded into the cell")
	}
}

func TestRenderMarkdownWithoutIncidents(t *testing.T) {
	var b strings.Builder
	RenderMarkdown(&b, nil, Period{})
	report := b.String()

	if !strings.Contains(report, "None. No anomaly was detected") {
		t.Error("expected an explicit empty result")
	}

	if !strings.Contains(report, "not\nthat the systems were healthy") {
		t.Error("expected the empty result to warn against reading it as health")
	}
}
