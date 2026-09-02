package correlate

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"github.com/bornholm/tezcatl/internal/core/model"
)

// TestMeasureSeverityOnCapturedEvents regrades a real day of events
// with the new rule. Point TEZCATL_EVENTS_CAPTURE at a JSONL capture;
// the capture stays outside the repository, being production logs.
func TestMeasureSeverityOnCapturedEvents(t *testing.T) {
	path := os.Getenv("TEZCATL_EVENTS_CAPTURE")
	if path == "" {
		t.Skip("set TEZCATL_EVENTS_CAPTURE to a JSONL capture of events to run this")
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	was := map[model.Severity]int{}
	now := map[model.Severity]int{}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		var event model.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}

		multimodal := event.Attributes["multimodal"] == "true"

		was[event.Severity]++
		now[severityOf(event.Confidence, event.Signals, multimodal, len(event.RelatedChanges) > 0)]++
	}

	for _, severity := range []model.Severity{model.SeverityCritical, model.SeverityWarning, model.SeverityInfo} {
		t.Logf("%-9s avant %3d  après %3d", severity, was[severity], now[severity])
	}
}
