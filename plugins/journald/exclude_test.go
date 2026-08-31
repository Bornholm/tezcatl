package main

import (
	"testing"

	"github.com/bornholm/tezcatl/plugins/journald/internal/journal"
)

func TestIsExcluded(t *testing.T) {
	for _, test := range []struct {
		name     string
		entry    journal.Entry
		patterns []string
		want     bool
	}{
		{
			// The loop the default exists for: the server's own output
			// carries every event it ever produced.
			name:     "the server's own unit",
			entry:    journal.Entry{Unit: "tezcatl-server.service"},
			patterns: defaultExcludedUnits(),
			want:     true,
		},
		{
			name:     "a templated ingest unit",
			entry:    journal.Entry{Unit: "tezcatl-ingest@blog.service"},
			patterns: defaultExcludedUnits(),
			want:     true,
		},
		{
			// systemd journals the lifecycle of tezcatl's units under
			// its own identity, which is how a restart stays visible.
			name:     "systemd reporting on a tezcatl unit",
			entry:    journal.Entry{Unit: "init.scope", Identifier: "systemd"},
			patterns: defaultExcludedUnits(),
		},
		{
			name:     "an unrelated unit",
			entry:    journal.Entry{Unit: "nginx.service"},
			patterns: defaultExcludedUnits(),
		},
		{
			name:     "an entry named only by its syslog identifier",
			entry:    journal.Entry{Identifier: "tezcatl"},
			patterns: defaultExcludedUnits(),
			want:     true,
		},
		{
			name:  "no patterns excludes nothing",
			entry: journal.Entry{Unit: "tezcatl-server.service"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isExcluded(test.entry, test.patterns); got != test.want {
				t.Errorf("expected excluded to be %t, got %t", test.want, got)
			}
		})
	}
}

func TestExcludedUnits(t *testing.T) {
	empty := []string{}

	for _, test := range []struct {
		name string
		cfg  config
		want int
	}{
		{
			name: "reading the whole journal keeps tezcatl out of it",
			cfg:  config{},
			want: len(defaultExcludedUnits()),
		},
		{
			// Naming units is an intent, and a default does not
			// overrule an intent.
			name: "an explicit allow list drops the default",
			cfg:  config{Units: []string{"tezcatl-server.service"}},
		},
		{
			name: "an explicit empty exclusion means exclude nothing",
			cfg:  config{ExcludeUnits: &empty},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := len(excludedUnits(test.cfg)); got != test.want {
				t.Errorf("expected %d exclusion patterns, got %d", test.want, got)
			}
		})
	}
}
