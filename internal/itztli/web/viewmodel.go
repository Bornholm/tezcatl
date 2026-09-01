package web

import (
	"fmt"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/incident"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/itztli/client"
)

// Nav is what the shell around every page needs.
type Nav struct {
	Version string
	Target  string
	Window  time.Duration
	Active  string
}

// WindowLabel writes the incident window for the nav: "fenêtre 15 j".
func (n Nav) WindowLabel() string {
	if days := int(n.Window / (24 * time.Hour)); days >= 1 && n.Window%(24*time.Hour) == 0 {
		return fmt.Sprintf("fenêtre %d j", days)
	}

	return "fenêtre " + FrenchDuration(n.Window)
}

// WindowContext names the period in page subtitles: "15 derniers
// jours".
func (n Nav) WindowContext() string {
	if days := int(n.Window / (24 * time.Hour)); days >= 1 && n.Window%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d derniers jours", days)
	}

	return "dernières " + FrenchDuration(n.Window)
}

// Login is the login page in either mode.
type Login struct {
	// Mode is "password" or "oidc".
	Mode        string
	ButtonLabel string
	// Error is a message from a failed attempt.
	Error string
}

// IncidentCard is one entry of the dashboard list.
type IncidentCard struct {
	ID           string
	Title        string
	Severity     Severity
	TriggerKind  string
	TriggerParts []Part
	Services     string
	RelPeriod    string
	EventsLabel  string
	// ChangeLabel is empty when no change is correlated.
	ChangeLabel string
}

func NewIncidentCard(entry incident.Incident, now time.Time) IncidentCard {
	relPeriod := Ago(entry.Start, now)
	if duration := entry.End.Sub(entry.Start); duration >= time.Second {
		relPeriod += ", pendant " + FrenchDuration(duration)
	}

	card := IncidentCard{
		ID:           client.IncidentID(entry),
		Title:        entry.Title,
		Severity:     SeverityOf(entry.Severity),
		TriggerKind:  strings.TrimPrefix(entry.Trigger.Kind, "anomaly."),
		TriggerParts: MaskParts(entry.Trigger.Summary),
		Services:     strings.Join(entry.Services, ", "),
		RelPeriod:    relPeriod,
		EventsLabel:  Plural(len(entry.Events), "événement"),
	}

	if len(entry.Changes) > 0 {
		change := entry.Changes[0]
		when := FrenchDuration(change.BeforeStart) + " avant"
		if change.BeforeStart < 0 {
			when = FrenchDuration(-change.BeforeStart) + " après"
		}

		card.ChangeLabel = fmt.Sprintf("%s corrélé %s", change.Type, when)
	}

	return card
}

// EmptyState carries the honest numbers shown when there is no
// incident: what the server did receive and learn.
type EmptyState struct {
	TotalEvents   int
	Templates     int
	WarmupMetrics int
}

func (e EmptyState) StatsLabel() string {
	return fmt.Sprintf("%s reçus · %s appris · %s en apprentissage",
		Plural(e.TotalEvents, "événement"),
		Plural(e.Templates, "template"),
		Plural(e.WarmupMetrics, "série"))
}

// IncidentDetail is the full story of one incident.
type IncidentDetail struct {
	ID             string
	Title          string
	Severity       Severity
	ConfLabel      string
	AbsPeriod      string
	Services       string
	TriggerSummary string
	TriggerTime    string
	TriggerKind    string
	Changes        []ChangeView
	Evidence       []EvidenceView
	ContextLines   []string
	Events         []EventView
}

type ChangeView struct {
	Type    string
	Version string
	Delta   string
}

type EvidenceView struct {
	Kind       string
	Source     string
	CountLabel string
	Span       string
	MaxScore   string
	Summary    string
}

type EventView struct {
	Time    string
	Kind    string
	Class   string
	Summary string
}

func NewIncidentDetail(entry incident.Incident) IncidentDetail {
	absPeriod := AbsPeriod(entry.Start, entry.End)
	if duration := entry.End.Sub(entry.Start); duration >= time.Second {
		absPeriod += " · pendant " + FrenchDuration(duration)
	}

	detail := IncidentDetail{
		ID:             client.IncidentID(entry),
		Title:          entry.Title,
		Severity:       SeverityOf(entry.Severity),
		ConfLabel:      "confiance " + FormatFloat(entry.Confidence),
		AbsPeriod:      absPeriod,
		Services:       strings.Join(entry.Services, ", "),
		TriggerSummary: entry.Trigger.Summary,
		TriggerTime:    entry.Trigger.Timestamp.Local().Format("15:04:05 (UTC-07:00)"),
		TriggerKind:    entry.Trigger.Kind,
		ContextLines:   contextLines(entry.Trigger),
	}

	for _, change := range entry.Changes {
		delta := FrenchDuration(change.BeforeStart) + " avant l'anomalie"
		if change.BeforeStart < 0 {
			delta = FrenchDuration(-change.BeforeStart) + " après l'anomalie"
		}

		detail.Changes = append(detail.Changes, ChangeView{
			Type:    change.Type,
			Version: change.Version,
			Delta:   delta,
		})
	}

	for _, evidence := range entry.Evidence {
		span := evidence.First.Local().Format("15:04:05")
		if evidence.Count > 1 && !evidence.Last.Equal(evidence.First) {
			span += " → " + evidence.Last.Local().Format("15:04:05")
		}

		detail.Evidence = append(detail.Evidence, EvidenceView{
			Kind:       evidence.Kind,
			Source:     evidence.Source,
			CountLabel: Plural(evidence.Count, "occurrence"),
			Span:       span,
			MaxScore:   FormatFloat(evidence.MaxScore),
			Summary:    evidence.Summary,
		})
	}

	for _, event := range entry.Events {
		detail.Events = append(detail.Events, EventView{
			Time:    event.Timestamp.Local().Format("15:04:05"),
			Kind:    event.Kind,
			Class:   SeverityOf(event.Severity).Class,
			Summary: event.Summary,
		})
	}

	return detail
}

// contextLines flattens the trigger's surrounding log observations,
// the raw lines an engineer would have scrolled to.
func contextLines(event model.Event) []string {
	lines := []string{}

	appendLogs := func(observations []model.Observation) {
		for _, observation := range observations {
			if observation.Log == nil {
				continue
			}

			lines = append(lines, fmt.Sprintf("%s  %s",
				observation.Timestamp.Local().Format("15:04:05"),
				observation.Log.Raw))
		}
	}

	appendLogs(event.Context.Before)
	appendLogs(event.Context.After)

	return lines
}

// ExplainView is the generated-explanation panel in one of its states.
type ExplainView struct {
	IncidentID string
	Model      string
	// State is "idle" (nothing asked yet), "pending" (the model is
	// answering) or "done".
	State string
	// Text is the model's reading, once done.
	Text string
	// Error is the failure to reach or use the model.
	Error string
}

// Explain state names, matching the job the server runs.
const (
	ExplainIdle         = "idle"
	ExplainPendingState = "pending"
	ExplainDone         = "done"
)

// TemplateRow is one learned template with its marking actions.
type TemplateRow struct {
	Partition string
	Template  string
	Parts     []Part
	Size      int64
	Marking   string
	SubLabel  string
}

func NewTemplateRow(template client.Template) TemplateRow {
	marking := template.Marking
	if marking == "" {
		marking = "non marqué"
	}

	occurrences := "occurrences"
	if template.Size == 1 {
		occurrences = "occurrence"
	}

	return TemplateRow{
		Partition: template.Partition,
		Template:  template.Template,
		Parts:     MaskParts(template.Template),
		Size:      template.Size,
		Marking:   template.Marking,
		SubLabel:  fmt.Sprintf("%s · %s %s · %s", template.Partition, FormatInt(template.Size), occurrences, marking),
	}
}

// MetricRow is one learned series with its deviation reading.
type MetricRow struct {
	Key          string
	Ignored      bool
	Warmup       bool
	StatsLabel   string
	Recent       string
	SigmaLabel   string
	DevClass     string
	BarWidth     string
	CompareLabel string
	ButtonLabel  string
}

func NewMetricRow(metric client.Metric) MetricRow {
	sigma := metric.Sigma()

	absSigma := sigma
	if absSigma < 0 {
		absSigma = -absSigma
	}

	devClass := "norm"
	switch {
	case metric.Ignored:
		devClass = "muted"
	case metric.Warmup:
		devClass = "norm"
	case absSigma >= 3:
		devClass = "hot"
	case absSigma >= 1:
		devClass = "warm"
	}

	sigmaLabel := "apprentissage"
	if !metric.Warmup {
		sign := "+"
		if sigma < 0 {
			// U+2212, the actual minus sign.
			sign = "−"
		}

		sigmaLabel = fmt.Sprintf("%s%s σ", sign, FormatFloat(round1(absSigma)))
	}

	stats := fmt.Sprintf("%s relevés · moyenne %s · écart type %s",
		FormatInt(metric.Samples), FormatFloat(metric.Mean), FormatFloat(metric.StdDev))
	if metric.Warmup {
		stats += " · en apprentissage"
	}
	if metric.Ignored {
		stats += " · ignorée"
	}

	ratio := absSigma / 12
	if ratio > 1 {
		ratio = 1
	}
	width := ratio * 100
	if width < 3 {
		width = 3
	}

	buttonLabel := "ignorer"
	if metric.Ignored {
		buttonLabel = "cesser d'ignorer"
	}

	return MetricRow{
		Key:          metric.Key,
		Ignored:      metric.Ignored,
		Warmup:       metric.Warmup,
		StatsLabel:   stats,
		Recent:       FormatFloat(metric.Recent),
		SigmaLabel:   sigmaLabel,
		DevClass:     devClass,
		BarWidth:     fmt.Sprintf("%.0f%%", width),
		CompareLabel: fmt.Sprintf("moyenne apprise %s · écart %s", FormatFloat(metric.Mean), FormatFloat(metric.Recent-metric.Mean)),
		ButtonLabel:  buttonLabel,
	}
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
