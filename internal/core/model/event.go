package model

import "time"

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Event is the contextualized output of the pipeline, meant to be
// actionable by an operator or an LLM agent without access to the raw
// streams.
type Event struct {
	ID             string            `json:"id"`
	Kind           string            `json:"kind"`
	Source         string            `json:"source"`
	Service        string            `json:"service,omitempty"`
	Environment    string            `json:"environment,omitempty"`
	Timestamp      time.Time         `json:"timestamp"`
	Severity       Severity          `json:"severity"`
	Confidence     float64           `json:"confidence"`
	Summary        string            `json:"summary"`
	Signals        []Signal          `json:"signals,omitempty"`
	RelatedChanges []RelatedChange   `json:"related_changes,omitempty"`
	Context        Context           `json:"context,omitzero"`
	Attributes     map[string]string `json:"attributes,omitempty"`
}

// RelatedChange is a change observed close enough to an event to be
// worth surfacing. It is a temporal correlation, not a proof of cause.
type RelatedChange struct {
	Source        string       `json:"source"`
	Change        ChangeRecord `json:"change"`
	Timestamp     time.Time    `json:"timestamp"`
	OffsetSeconds float64      `json:"offset_seconds"`
}

// Signal is an elementary anomaly produced by a detector, before
// correlation aggregates related signals into a single event.
type Signal struct {
	Kind       string            `json:"kind"`
	Modality   Modality          `json:"modality"`
	Source     string            `json:"source"`
	Timestamp  time.Time         `json:"timestamp"`
	Score      float64           `json:"score"`
	Summary    string            `json:"summary"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Context carries the observations surrounding an anomaly.
type Context struct {
	Before []Observation `json:"before,omitempty"`
	After  []Observation `json:"after,omitempty"`
}
