package web

import (
	"fmt"
	"net/url"
	"time"
)

// IncidentFilter is what the reader asked the list for. It travels in
// the URL rather than in a session, so a narrowed list can be
// bookmarked and shared: "the criticals of the last six hours" is a
// link.
type IncidentFilter struct {
	// Range is how far back to look; 0 means the whole window.
	Range time.Duration
	// Severity is the minimum severity kept: critical, warning, info
	// or all.
	Severity string
	// Gap is the silence after which an incident is over.
	Gap time.Duration
}

// Choice is one option of a filter row.
type Choice struct {
	Label  string
	Value  string
	Active bool
}

// Query renders the filter as URL parameters, for the links that must
// keep it: pagination, mostly.
func (f IncidentFilter) Query() string {
	values := url.Values{}
	values.Set("range", durationParam(f.Range))
	values.Set("severity", f.Severity)
	values.Set("gap", durationParam(f.Gap))

	return values.Encode()
}

func durationParam(d time.Duration) string {
	if d <= 0 {
		return "all"
	}

	return d.String()
}

// RangeChoices offers the usual lookbacks, dropping the ones the
// server could not answer: the window bounds what was fetched.
func (f IncidentFilter) RangeChoices(window time.Duration) []Choice {
	candidates := []struct {
		label string
		value time.Duration
	}{
		{"1 h", time.Hour},
		{"6 h", 6 * time.Hour},
		{"24 h", 24 * time.Hour},
		{"7 j", 7 * 24 * time.Hour},
	}

	choices := []Choice{}
	for _, candidate := range candidates {
		if candidate.value >= window {
			break
		}

		choices = append(choices, Choice{
			Label:  candidate.label,
			Value:  candidate.value.String(),
			Active: f.Range == candidate.value,
		})
	}

	return append(choices, Choice{
		Label:  "tout (" + shortWindow(window) + ")",
		Value:  "all",
		Active: f.Range <= 0,
	})
}

// SeverityChoices reads as a floor, not as an exact match: warning
// shows the warnings and everything worse.
func (f IncidentFilter) SeverityChoices() []Choice {
	return []Choice{
		{Label: "critique", Value: "critical", Active: f.Severity == "critical"},
		{Label: "warning et +", Value: "warning", Active: f.Severity == "warning"},
		{Label: "tout", Value: "all", Active: f.Severity == "all" || f.Severity == "" || f.Severity == "info"},
	}
}

// GapChoices tunes what counts as one story: a short gap splits a long
// night into separate incidents, a long one merges them.
func (f IncidentFilter) GapChoices() []Choice {
	choices := []Choice{}

	for _, gap := range []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour} {
		choices = append(choices, Choice{
			Label:  FrenchDuration(gap),
			Value:  gap.String(),
			Active: f.Gap == gap,
		})
	}

	return choices
}

// RangeLabel names the current range for the page subtitle.
func (f IncidentFilter) RangeLabel(window time.Duration) string {
	if f.Range <= 0 {
		return "sur " + shortWindow(window)
	}

	if f.Range >= 24*time.Hour && f.Range%(24*time.Hour) == 0 {
		if days := int(f.Range / (24 * time.Hour)); days == 1 {
			return "dernières 24 h"
		} else {
			return fmt.Sprintf("%d derniers jours", days)
		}
	}

	return "dernières " + FrenchDuration(f.Range)
}

// SeverityLabel names the current severity floor for the subtitle.
func (f IncidentFilter) SeverityLabel() string {
	switch f.Severity {
	case "critical":
		return "critiques seulement"
	case "warning":
		return "warning et au-dessus"
	default:
		return "toutes sévérités"
	}
}

func shortWindow(window time.Duration) string {
	if days := int(window / (24 * time.Hour)); days >= 1 && window%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d j", days)
	}

	return FrenchDuration(window)
}
