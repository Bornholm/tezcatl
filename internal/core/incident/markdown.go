package incident

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/detect"
)

// RenderMarkdown writes a report meant to be handed to an LLM agent for
// diagnosis. It leads with the schema, because a reader who does not
// know that "<NUM>" is a mask, that evidence lines are aggregated, or
// that a related change is a coincidence in time and nothing more, will
// confidently misread the data. The preamble is written once, however
// many incidents follow.
func RenderMarkdown(w io.Writer, incidents []Incident, period Period) {
	fmt.Fprintln(w, "# Tezcatl incident report")
	fmt.Fprintln(w)

	writeScope(w, incidents, period)
	writeSchema(w)

	if len(incidents) == 0 {
		fmt.Fprintln(w, "## Incidents")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "None. No anomaly was detected in this period. This means the")
		fmt.Fprintln(w, "detectors saw nothing unusual against what they have learned, not")
		fmt.Fprintln(w, "that the systems were healthy: a failure that looks like the")
		fmt.Fprintln(w, "learned normal produces no anomaly.")

		return
	}

	for i, entry := range incidents {
		writeIncident(w, i+1, entry)
	}
}

// Period is the window a report covers, for the scope section.
type Period struct {
	Since time.Time
	Until time.Time
	// Generated is when the report was produced.
	Generated time.Time
}

func writeScope(w io.Writer, incidents []Incident, period Period) {
	fmt.Fprintln(w, "## Scope")
	fmt.Fprintln(w)

	if !period.Generated.IsZero() {
		fmt.Fprintf(w, "- Generated: %s\n", period.Generated.Format(time.RFC3339))
	}

	window := "the server's whole retained history"
	switch {
	case !period.Since.IsZero() && !period.Until.IsZero():
		window = fmt.Sprintf("%s to %s", period.Since.Format(time.RFC3339), period.Until.Format(time.RFC3339))
	case !period.Since.IsZero():
		window = fmt.Sprintf("since %s", period.Since.Format(time.RFC3339))
	case !period.Until.IsZero():
		window = fmt.Sprintf("up to %s", period.Until.Format(time.RFC3339))
	}

	fmt.Fprintf(w, "- Period covered: %s\n", window)
	fmt.Fprintf(w, "- Incidents in this report: %d\n", len(incidents))
	fmt.Fprintln(w)
}

func writeSchema(w io.Writer) {
	fmt.Fprint(w, `## How to read this report

Tezcatl watches logs, metrics and declared changes. It learns what each
service normally does, then reports what departs from it. This report is
its output; the notes below are what you need to read the data without
inventing meaning that is not there.

### The pipeline behind the data

1. **Observations** arrive: log lines, metric samples, change records.
2. Log lines are clustered into **templates** (a Drain3 port). A template
   is a line with its variable parts replaced by placeholders.
3. **Detectors** compare each observation to the baseline learned for its
   own series or template, and emit **signals**.
4. Signals close in time on the same service are merged into **events**.
5. This report groups events into **incidents**.

### Placeholders in templates

Template text contains masks, not literal values. ` + "`<NUM>`" + `, ` + "`<IP>`" + `,
` + "`<HEX>`" + `, ` + "`<UUID>`" + `, ` + "`<EMAIL>`" + ` stand for values that were masked
before clustering, and ` + "`<*>`" + ` marks a position where the clustering found
varying content. A template reading ` + "`connection timeout after <NUM>s`" + `
matched many lines with different numbers. Do not read a placeholder as
a literal, and do not infer the masked value.

### What each field means

- **Trigger**: the first event of the incident. First in time, which is
  not the same as root cause.
- **Evidence**: every signal of the incident, folded by kind and source.
  A count of x14 means fourteen occurrences, and the summary shown is the
  strongest single occurrence, not an average.
- **Severity**: the worst severity among the incident's events. It comes
  from the detectors' own scoring, not from any judgment about impact.
- **Confidence**: how sure the detector is that this is a departure from
  the baseline. It says nothing about how serious the departure is.
- **Related changes**: deployments, restarts and configuration changes
  that were declared to tezcatl and that fall near the incident in time.
  **This is correlation, and only correlation.** Tezcatl attaches them
  because an engineer would want to see them, not because it has any
  evidence they caused anything.

### Signal kinds

| Kind | Meaning |
| --- | --- |
`)

	for _, entry := range signalGlossary() {
		fmt.Fprintf(w, "| `%s` | %s |\n", entry.kind, entry.meaning)
	}

	fmt.Fprint(w, `
### What tezcatl does not know

- **It does not know causality.** Nothing here establishes that one thing
  caused another, including the related changes.
- **It only sees what it ingests.** A service with no log or metric
  collection is invisible; its silence in this report means nothing.
- **A quiet detector is not a healthy system.** Failures that resemble the
  learned normal produce no signal, and a service that has been broken
  since the baseline was learned looks normal.
- **Statistically extreme can be operationally trivial.** Near-constant
  series produce huge z-scores on tiny moves, so significance floors
  suppress deviations below a configured absolute delta. Signals you do
  not see here may have been filtered on purpose.
- **Incident boundaries are a heuristic.** Events are grouped when they
  share a service, share a related change, or land in the same collection
  cycle, with a maximum duration. Two incidents may be one story, and one
  incident may hold two unrelated stories.
- **Timestamps are the observations' own.** They come from the monitored
  systems, so clock skew between hosts is possible.

### What is useful to do with this

State what the data supports, separately from what it merely suggests.
Name the alternatives you cannot rule out with what is here, and say which
extra observation would settle them. If the evidence does not identify a
cause, say so rather than picking the most plausible-looking candidate.

`)
}

type glossaryEntry struct {
	kind    string
	meaning string
}

func signalGlossary() []glossaryEntry {
	return []glossaryEntry{
		{detect.SignalLogNewTemplate, "A log line whose shape had never been seen since the learning period ended."},
		{detect.SignalLogRareTemplate, "A template seen very few times relative to the volume of the source."},
		{detect.SignalLogFrequencySpike, "A template occurring far more often than its learned rate for this hour of day."},
		{detect.SignalLogMissingTemplate, "A regular template that has not appeared for much longer than its usual interval. Often means a component stopped, not that a message was lost."},
		{detect.SignalLogSymptomatic, "A template an operator explicitly marked as a symptom worth reporting."},
		{detect.SignalMetricZScore, "A sample far from the series' learned mean, measured in standard deviations (z). The summary gives value, baseline and z."},
		{detect.SignalMetricThreshold, "A sample beyond a static bound an operator configured. Unlike the others, this one encodes a human's intent."},
		{detect.SignalMetricTrendDrift, "A fast moving average diverging from a slow one: a sustained drift rather than a spike."},
	}
}

func writeIncident(w io.Writer, number int, incident Incident) {
	fmt.Fprintf(w, "## Incident %d — %s\n\n", number, incident.Title)

	duration := incident.End.Sub(incident.Start).Round(time.Second)

	fmt.Fprintf(w, "- Started: %s\n", incident.Start.Format(time.RFC3339))
	fmt.Fprintf(w, "- Last activity: %s (%s later)\n", incident.End.Format(time.RFC3339), duration)
	fmt.Fprintf(w, "- Severity: %s\n", incident.Severity)
	fmt.Fprintf(w, "- Confidence: %.2f\n", incident.Confidence)
	fmt.Fprintf(w, "- Services involved, in order of appearance: %s\n", strings.Join(incident.Services, ", "))
	fmt.Fprintf(w, "- Events grouped: %d\n\n", len(incident.Events))

	fmt.Fprintln(w, "### Trigger")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "The first event of this incident, at %s on `%s`:\n\n",
		incident.Trigger.Timestamp.Format(time.RFC3339), serviceOf(incident.Trigger))
	fmt.Fprintf(w, "> %s\n\n", escapeCell(incident.Trigger.Summary))

	if len(incident.Changes) > 0 {
		fmt.Fprintln(w, "### Changes near this incident (correlation only)")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "| When | Position | Type | Version | Summary |")
		fmt.Fprintln(w, "| --- | --- | --- | --- | --- |")

		for _, change := range incident.Changes {
			position := fmt.Sprintf("%s before the trigger", change.BeforeStart.Round(time.Second))
			if change.BeforeStart < 0 {
				position = fmt.Sprintf("%s after the trigger", (-change.BeforeStart).Round(time.Second))
			}

			fmt.Fprintf(w, "| %s | %s | %s | %s | %s |\n",
				change.Timestamp.Format(time.RFC3339), position,
				escapeCell(change.Type), escapeCell(change.Version), escapeCell(change.Summary))
		}

		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "### Evidence")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| First seen | Kind | Source | Occurrences | Max score | Strongest occurrence |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- |")

	for _, entry := range incident.Evidence {
		occurrences := fmt.Sprintf("%d", entry.Count)
		if entry.Count > 1 {
			occurrences = fmt.Sprintf("%d, last at %s", entry.Count, entry.Last.Format("15:04:05Z07:00"))
		}

		fmt.Fprintf(w, "| %s | `%s` | `%s` | %s | %.2f | %s |\n",
			entry.First.Format(time.RFC3339), entry.Kind, escapeCell(entry.Source),
			occurrences, entry.MaxScore, escapeCell(entry.Summary))
	}

	fmt.Fprintln(w)
}

// escapeCell keeps log lines from breaking the table they sit in:
// pipes end cells and newlines end rows.
func escapeCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")

	return strings.TrimSpace(value)
}
