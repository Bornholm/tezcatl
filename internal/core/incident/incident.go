package incident

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

// Incident is the story an engineer reconstructs by hand during an
// outage, assembled from the events of one burst of anomalies: what
// fired first, what else was going wrong, and what had changed shortly
// before. It distinguishes the trigger from the evidence, and keeps the
// changes clearly labeled as correlation.
type Incident struct {
	Title      string         `json:"title"`
	Severity   model.Severity `json:"severity"`
	Confidence float64        `json:"confidence"`
	Start      time.Time      `json:"start"`
	End        time.Time      `json:"end"`
	// Services are the identities involved, in order of appearance:
	// the first one saw the trigger.
	Services []string `json:"services"`
	// Trigger is the first event of the burst.
	Trigger model.Event `json:"trigger"`
	// Evidence aggregates every signal of the burst by kind and
	// source, so ten occurrences of the same spike read as one line.
	Evidence []Evidence `json:"evidence"`
	// Changes are the deployments, restarts and configuration changes
	// correlated with the burst: context, not proof of cause.
	Changes []Change `json:"related_changes,omitempty"`
	// Events are the underlying correlated events, oldest first.
	Events []model.Event `json:"events"`
}

// Evidence is one kind of signal observed on one source during the
// incident.
type Evidence struct {
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Count   int    `json:"count"`
	// Summary is the strongest occurrence's own words.
	Summary  string    `json:"summary"`
	MaxScore float64   `json:"max_score"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
}

// Change is a related change replaced on the incident's own clock.
type Change struct {
	Type      string    `json:"type"`
	Version   string    `json:"version,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	// BeforeStart is how long before the trigger the change happened;
	// negative when it happened during the incident.
	BeforeStart time.Duration `json:"-"`
}

// DefaultGap separates two incidents: a lull this long means whatever
// comes next is a new story. Half an hour trades a few merged storms
// for never splitting one incident in two.
const DefaultGap = 30 * time.Minute

// Group clusters events into incidents: consecutive events closer than
// gap belong to the same burst. Events must not contain debug noise;
// the caller filters kinds. Incidents come back oldest first.
func Group(events []model.Event, gap time.Duration) []Incident {
	if gap <= 0 {
		gap = DefaultGap
	}

	sorted := make([]model.Event, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})

	incidents := []Incident{}

	var burst []model.Event
	for _, event := range sorted {
		if len(burst) > 0 && event.Timestamp.Sub(burst[len(burst)-1].Timestamp) > gap {
			incidents = append(incidents, build(burst))
			burst = nil
		}

		burst = append(burst, event)
	}

	if len(burst) > 0 {
		incidents = append(incidents, build(burst))
	}

	return incidents
}

func build(events []model.Event) Incident {
	incident := Incident{
		Trigger: events[0],
		Start:   events[0].Timestamp,
		End:     events[len(events)-1].Timestamp,
		Events:  events,
	}

	seenService := map[string]bool{}
	evidence := map[string]*Evidence{}
	changes := map[string]*Change{}

	for _, event := range events {
		if severityRank(event.Severity) >= severityRank(incident.Severity) {
			incident.Severity = event.Severity
		}

		if event.Confidence > incident.Confidence {
			incident.Confidence = event.Confidence
		}

		service := serviceOf(event)
		if !seenService[service] {
			seenService[service] = true
			incident.Services = append(incident.Services, service)
		}

		for _, signal := range event.Signals {
			key := signal.Kind + "\x00" + signal.Source

			entry, exists := evidence[key]
			if !exists {
				entry = &Evidence{
					Kind:   signal.Kind,
					Source: signal.Source,
					First:  signal.Timestamp,
					Last:   signal.Timestamp,
				}
				evidence[key] = entry
			}

			entry.Count++

			if signal.Score >= entry.MaxScore {
				entry.MaxScore = signal.Score
				entry.Summary = signal.Summary
			}

			if signal.Timestamp.Before(entry.First) {
				entry.First = signal.Timestamp
			}

			if signal.Timestamp.After(entry.Last) {
				entry.Last = signal.Timestamp
			}
		}

		for _, related := range event.RelatedChanges {
			timestamp := event.Timestamp.Add(time.Duration(related.OffsetSeconds * float64(time.Second)))
			key := related.Change.Type + "\x00" + related.Change.Version + "\x00" + related.Change.Summary

			if _, exists := changes[key]; !exists {
				changes[key] = &Change{
					Type:      related.Change.Type,
					Version:   related.Change.Version,
					Summary:   related.Change.Summary,
					Timestamp: timestamp,
				}
			}
		}
	}

	for _, entry := range evidence {
		incident.Evidence = append(incident.Evidence, *entry)
	}

	sort.Slice(incident.Evidence, func(i, j int) bool {
		if !incident.Evidence[i].First.Equal(incident.Evidence[j].First) {
			return incident.Evidence[i].First.Before(incident.Evidence[j].First)
		}

		return incident.Evidence[i].Kind < incident.Evidence[j].Kind
	})

	for _, change := range changes {
		change.BeforeStart = incident.Start.Sub(change.Timestamp)
		incident.Changes = append(incident.Changes, *change)
	}

	sort.Slice(incident.Changes, func(i, j int) bool {
		return incident.Changes[i].Timestamp.Before(incident.Changes[j].Timestamp)
	})

	incident.Title = title(incident)

	return incident
}

// title names the incident after its trigger, then says how far it
// spread.
func title(incident Incident) string {
	label := serviceOf(incident.Trigger)

	summary := incident.Trigger.Summary
	if cut := strings.IndexAny(summary, "("); cut > 0 {
		summary = summary[:cut]
	}

	summary = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(summary), ":"))

	if others := len(incident.Services) - 1; others > 0 {
		return fmt.Sprintf("%s: %s (+%d service%s)", label, summary, others, plural(others))
	}

	return fmt.Sprintf("%s: %s", label, summary)
}

func plural(count int) string {
	if count > 1 {
		return "s"
	}

	return ""
}

func severityRank(severity model.Severity) int {
	switch severity {
	case model.SeverityCritical:
		return 3
	case model.SeverityWarning:
		return 2
	case model.SeverityInfo:
		return 1
	default:
		return 0
	}
}

func serviceOf(event model.Event) string {
	if event.Service != "" {
		return event.Service
	}

	return event.Source
}
