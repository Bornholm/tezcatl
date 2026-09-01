package incident

import (
	"fmt"
	"io"
	"strings"

	"github.com/bornholm/tezcatl/internal/core/humanize"
)

// Render writes the incident the way an engineer would brief it: the
// headline, what changed beforehand, what fired in what order, and the
// aggregated evidence. Plain text, no markup, safe for a terminal, a
// ticket or an LLM prompt.
func Render(w io.Writer, incident Incident) {
	fmt.Fprintf(w, "incident: %s\n", incident.Title)

	duration := incident.End.Sub(incident.Start)
	span := incident.Start.Local().Format("2006-01-02 15:04:05")
	if duration > 0 {
		span += fmt.Sprintf(" -> %s (%s)", incident.End.Local().Format("15:04:05"), humanize.Duration(duration))
	}

	fmt.Fprintf(w, "severity: %s (confidence %.2f)\n", incident.Severity, incident.Confidence)
	fmt.Fprintf(w, "when:     %s\n", span)
	fmt.Fprintf(w, "services: %s\n", strings.Join(incident.Services, ", "))

	if len(incident.Changes) > 0 {
		fmt.Fprintf(w, "\nchanges before or during (correlation, not causation):\n")

		for _, change := range incident.Changes {
			position := fmt.Sprintf("%s before the trigger", humanize.Duration(change.BeforeStart))
			if change.BeforeStart < 0 {
				position = fmt.Sprintf("%s into the incident", humanize.Duration(-change.BeforeStart))
			}

			fmt.Fprintf(w, "  %s  %s %s  (%s)%s\n",
				change.Timestamp.Local().Format("15:04:05"),
				change.Type, change.Version, position, optional(" — ", change.Summary))
		}
	}

	fmt.Fprintf(w, "\ntrigger:\n  %s  %s  %s\n",
		incident.Trigger.Timestamp.Local().Format("15:04:05"),
		serviceOf(incident.Trigger), incident.Trigger.Summary)

	if len(incident.Evidence) > 0 {
		fmt.Fprintf(w, "\nevidence:\n")

		for _, entry := range incident.Evidence {
			occurrences := ""
			if entry.Count > 1 {
				occurrences = fmt.Sprintf(" x%d until %s", entry.Count, entry.Last.Local().Format("15:04:05"))
			}

			fmt.Fprintf(w, "  %s  %-26s %s%s\n      %s\n",
				entry.First.Local().Format("15:04:05"), entry.Kind, entry.Source, occurrences, entry.Summary)
		}
	}
}

func optional(prefix string, value string) string {
	if value == "" {
		return ""
	}

	return prefix + value
}
