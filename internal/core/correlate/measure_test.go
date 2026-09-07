package correlate

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

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

	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	for _, severity := range []model.Severity{model.SeverityCritical, model.SeverityWarning, model.SeverityInfo} {
		t.Logf("%-9s avant %3d  après %3d", severity, was[severity], now[severity])
	}
}

// TestMeasureEchoOnCapturedEvents replays a real capture through the
// correlator on the event clock: each event's signals go back in as
// they came, each related change is declared again, and the count of
// events out is what the echo fold would have made of those days.
// Point TEZCATL_EVENTS_CAPTURE at a JSONL capture; the capture stays
// outside the repository, being production logs.
func TestMeasureEchoOnCapturedEvents(t *testing.T) {
	path := os.Getenv("TEZCATL_EVENTS_CAPTURE")
	if path == "" {
		t.Skip("set TEZCATL_EVENTS_CAPTURE to a JSONL capture of events to run this")
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	type step struct {
		at     time.Time
		change *model.Observation
		event  *model.Event
	}

	steps := []step{}
	changes := map[string]bool{}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		var event model.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}

		for _, related := range event.RelatedChanges {
			key := related.Source + related.Timestamp.String()
			if changes[key] {
				continue
			}
			changes[key] = true

			change := related.Change
			declare := func(at time.Time) {
				steps = append(steps, step{at: at, change: &model.Observation{
					ID:          model.NewID(),
					Source:      related.Source,
					Service:     event.Service,
					Environment: event.Environment,
					Modality:    model.ModalityChange,
					Timestamp:   at,
					Change:      &change,
				}})
			}

			declare(related.Timestamp)

			// A capture taken before the build was declared only has
			// the post-deploy change, which comes after most of the
			// wake. TEZCATL_MEASURE_BUILD_LEAD (a duration) declares a
			// build start that long before each deploy, to estimate
			// what the pre-build hook adds.
			if lead, err := time.ParseDuration(os.Getenv("TEZCATL_MEASURE_BUILD_LEAD")); err == nil && lead > 0 {
				declare(related.Timestamp.Add(-lead))
			}
		}

		evt := event
		steps = append(steps, step{at: event.Timestamp, event: &evt})
	}

	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	sort.SliceStable(steps, func(i, j int) bool { return steps[i].at.Before(steps[j].at) })

	for _, echoWindow := range []time.Duration{0, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute} {
		config := DefaultConfig()
		config.Clock = ClockEvent
		config.EchoWindow = echoWindow

		correlator := NewCorrelator(config)

		out := map[string]int{}
		severities := map[model.Severity]int{}
		emit := func(evt model.Event) {
			out[evt.Kind]++
			severities[evt.Severity]++
		}

		for _, s := range steps {
			if s.change != nil {
				correlator.Observe(s.change)
				continue
			}

			obs := &model.Observation{
				ID:          model.NewID(),
				Source:      s.event.Source,
				Service:     s.event.Service,
				Environment: s.event.Environment,
				Modality:    model.ModalityLog,
				Timestamp:   s.event.Timestamp,
			}
			correlator.Observe(obs)
			correlator.Add(obs, s.event.Signals)
			correlator.Flush(false, emit)
		}

		correlator.Flush(true, emit)

		total := 0
		for _, n := range out {
			total += n
		}

		t.Logf("echo_window=%-4s events=%3d echoes=%2d critical=%d warning=%d info=%d",
			echoWindow, total, out["anomaly.change_echo"], severities[model.SeverityCritical], severities[model.SeverityWarning], severities[model.SeverityInfo])
	}
}
