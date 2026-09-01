package web

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/bornholm/tezcatl/internal/itztli/client"
)

var activeChip = regexp.MustCompile(`<button[^>]*class="chip-btn active"[^>]*>([^<]*)</button>`)

func activeChips(t *testing.T, markup string) []string {
	t.Helper()

	labels := []string{}
	for _, match := range activeChip.FindAllStringSubmatch(markup, -1) {
		labels = append(labels, match[1])
	}

	return labels
}

func renderRow(t *testing.T, marking string) string {
	t.Helper()

	var markup strings.Builder

	row := NewTemplateRow(client.Template{
		Partition: "demo/boutique",
		Template:  "INFO GET /api/cart 200 in <*>",
		Size:      320,
		Marking:   marking,
	})

	if err := TemplateRowView(row).Render(context.Background(), &markup); err != nil {
		t.Fatal(err)
	}

	return markup.String()
}

// TestEveryMarkingStateShowsWhichOneIsInForce covers the report that
// clearing a marking "does nothing": it did clear, but no chip lit up
// afterwards, so the click had no visible answer. Every state,
// including the default one, must show which chip is in force.
func TestEveryMarkingStateShowsWhichOneIsInForce(t *testing.T) {
	for marking, want := range map[string]string{
		"":            "défaut",
		"ignore":      "ignorer",
		"normal":      "normal",
		"symptomatic": "symptomatique",
	} {
		markup := renderRow(t, marking)

		active := activeChips(t, markup)
		if len(active) != 1 || active[0] != want {
			t.Errorf("marking %q lights up %v, want exactly [%s]", marking, active, want)
		}
	}
}

// TestMarkShortcutStatesAreLegible does the same for the shortcut on
// an incident, where a series has fewer actions than a template.
func TestMarkShortcutStatesAreLegible(t *testing.T) {
	render := func(target MarkTarget) string {
		var markup strings.Builder
		if err := MarkShortcut(target).Render(context.Background(), &markup); err != nil {
			t.Fatal(err)
		}

		return markup.String()
	}

	for name, test := range map[string]struct {
		target MarkTarget
		want   string
	}{
		"an unmarked template": {
			target: MarkTarget{Kind: "template", Value: "GET <*>"},
			want:   "défaut",
		},
		"a symptomatic template": {
			target: MarkTarget{Kind: "template", Value: "GET <*>", Marking: "symptomatic"},
			want:   "symptomatique",
		},
		// "normal" is a marking the shortcut does not offer, but the
		// detectors silence it like "ignore": showing nothing active
		// would read as untouched.
		"a template marked normal elsewhere": {
			target: MarkTarget{Kind: "template", Value: "GET <*>", Marking: "normal"},
			want:   "ignorer",
		},
		"an ignored series": {
			target: MarkTarget{Kind: "metric", Value: "prod/api/queue_depth", Marking: "ignore"},
			want:   "ignorer",
		},
	} {
		markup := render(test.target)

		active := activeChips(t, markup)
		if len(active) != 1 || active[0] != test.want {
			t.Errorf("%s lights up %v, want exactly [%s]", name, active, test.want)
		}
	}
}
