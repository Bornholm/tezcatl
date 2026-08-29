package tui

import (
	"math"
	"testing"

	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/bornholm/tezcatl/internal/core/detect"
)

func TestTemplateRows(t *testing.T) {
	templates := []admin.TemplateInfo{
		{Partition: "blog", ID: 1, Size: 5, Template: "GET <*> 200"},
		{Partition: "automata", ID: 2, Size: 10, Template: "tick <*>"},
		{Partition: "blog", ID: 3, Size: 50, Template: "POST <*> 500", Marking: detect.MarkingSymptomatic},
	}

	rows := templateRows(templates, "")

	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}

	// Grouped by partition, biggest first within a partition.
	if rows[0].Partition != "automata" {
		t.Errorf("expected automata first, got %s", rows[0].Partition)
	}

	if rows[1].ID != 3 || rows[2].ID != 1 {
		t.Errorf("expected blog templates ordered by size desc, got ids %d, %d", rows[1].ID, rows[2].ID)
	}

	filtered := templateRows(templates, "POST")
	if len(filtered) != 1 || filtered[0].ID != 3 {
		t.Errorf("expected only template 3 to match 'POST', got %+v", filtered)
	}

	byMarking := templateRows(templates, "symptomatic")
	if len(byMarking) != 1 || byMarking[0].ID != 3 {
		t.Errorf("expected marking to be searchable, got %+v", byMarking)
	}

	caseInsensitive := templateRows(templates, "AUTO")
	if len(caseInsensitive) != 1 || caseInsensitive[0].Partition != "automata" {
		t.Errorf("expected case-insensitive partition match, got %+v", caseInsensitive)
	}
}

func TestMetricRows(t *testing.T) {
	series := []detect.SeriesInfo{
		{Key: "host/system.cpu.percent", Samples: 100, Mean: 10, StdDev: 2, Recent: 10.5},
		{Key: "host/system.load1", Samples: 100, Mean: 1, StdDev: 0.1, Recent: 2},
		{Key: "host/container.memory.percent{name=blog}", Samples: 3, Warmup: true},
		{Key: "host/system.disk.percent", Samples: 100, Mean: 40, StdDev: 0, Recent: 40},
		{Key: "host/system.swap.percent", Samples: 100, Mean: 0, StdDev: 0, Recent: 5},
	}

	rows := metricRows(series, "")

	if len(rows) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(rows))
	}

	// Flat series that moved (infinite deviation) first, then by
	// deviation descending, warming-up series last.
	if rows[0].Key != "host/system.swap.percent" || !math.IsInf(rows[0].Deviation, 1) {
		t.Errorf("expected infinite deviation first, got %s (%v)", rows[0].Key, rows[0].Deviation)
	}

	if rows[1].Key != "host/system.load1" {
		t.Errorf("expected load1 (10 sigma) second, got %s", rows[1].Key)
	}

	if rows[2].Key != "host/system.cpu.percent" {
		t.Errorf("expected cpu (0.25 sigma) third, got %s", rows[2].Key)
	}

	if rows[3].Key != "host/system.disk.percent" || rows[3].Deviation != 0 {
		t.Errorf("expected flat unmoved series fourth, got %s (%v)", rows[3].Key, rows[3].Deviation)
	}

	if !math.IsNaN(rows[4].Deviation) {
		t.Errorf("expected warming-up series last, got %s", rows[4].Key)
	}

	filtered := metricRows(series, "load1")
	if len(filtered) != 1 || filtered[0].Key != "host/system.load1" {
		t.Errorf("expected only load1 to match, got %+v", filtered)
	}
}

func TestFormatDeviation(t *testing.T) {
	if got := formatDeviation(math.NaN()); got != "-" {
		t.Errorf("expected '-' for warmup, got %q", got)
	}

	if got := formatDeviation(math.Inf(1)); got != "inf" {
		t.Errorf("expected 'inf' for flat moved series, got %q", got)
	}

	if got := formatDeviation(2.345); got != "2.3σ" {
		t.Errorf("expected '2.3σ', got %q", got)
	}
}
