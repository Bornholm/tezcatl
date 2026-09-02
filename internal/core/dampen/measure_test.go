// Mesure hors ligne : combien des événements réels de l'instance
// survivraient à l'amortissement.
package dampen

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func TestMeasureOnInstanceEvents(t *testing.T) {
	// A capture of real events, kept outside the repository: it is
	// production log lines, and they belong on the machine that
	// produced them. Point TEZCATL_EVENTS_CAPTURE at one to measure
	// dampening against a real day.
	path := os.Getenv("TEZCATL_EVENTS_CAPTURE")
	if path == "" {
		t.Skip("set TEZCATL_EVENTS_CAPTURE to a JSONL capture of events to run this")
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	events := []model.Event{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var event model.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		events = append(events, event)
	}

	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	sort.Slice(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })

	for _, cooldown := range []time.Duration{0, 30 * time.Minute, time.Hour, 3 * time.Hour} {
		dampener := New(Config{Cooldown: cooldown})

		kept, keptCritical := 0, 0
		for _, event := range events {
			if len(dampener.Filter(event.Signals)) > 0 {
				kept++
				if event.Severity == model.SeverityCritical {
					keptCritical++
				}
			}
		}

		label := "sans amortissement"
		if cooldown > 0 {
			label = "cooldown " + cooldown.String()
		}

		t.Logf("%-22s %3d événements (%3d critiques)", label, kept, keptCritical)
	}
}
