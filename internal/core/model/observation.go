package model

import "time"

// Attributes set on log observations by the template mining stage.
const (
	AttrTemplateChangeType = "drain.change_type"
	AttrClusterSize        = "drain.cluster_size"
)

type Modality string

const (
	ModalityLog    Modality = "log"
	ModalityMetric Modality = "metric"
	ModalityChange Modality = "change"
	ModalityTrace  Modality = "trace"
)

// DefaultEnvironment is used when no environment is provided.
const DefaultEnvironment = "default"

// Observation is the normalized representation of any ingested datum,
// whatever its modality. Exactly one of the modality-specific payloads
// (Log, Metric, Change) is expected to be non-nil, matching Modality.
//
// Service and Environment form the canonical identity of the emitting
// system; Source ("<environment>/<service>") is derived from them by the
// normalization stage and is the partitioning and correlation key.
type Observation struct {
	ID          string            `json:"id"`
	Source      string            `json:"source"`
	Service     string            `json:"service,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Modality    Modality          `json:"modality"`
	Timestamp   time.Time         `json:"timestamp"`
	IngestedAt  time.Time         `json:"ingested_at"`
	Attributes  map[string]string `json:"attributes,omitempty"`

	Log    *LogRecord    `json:"log,omitempty"`
	Metric *MetricSample `json:"metric,omitempty"`
	Change *ChangeRecord `json:"change,omitempty"`
}

// PartitionKey identifies the sequential processing unit an observation
// belongs to: observations sharing a key are always handled by the same
// engine worker, in ingestion order. Partitioning by source keeps all
// modalities of a system on the same worker, which gives correlation a
// consistent view without cross-worker synchronization.
//
// Dispatching happens before normalization, so the key falls back to the
// raw identity when Source has not been derived yet.
func (o *Observation) PartitionKey() string {
	if o.Source != "" {
		return o.Source
	}

	return o.Environment + "/" + o.Service
}

type LogRecord struct {
	Raw string `json:"raw"`

	// Message is the payload extracted by log parsing (JSON envelope,
	// timestamp prefix removed); template mining works on it. Empty
	// means the raw line is the message.
	Message string `json:"message,omitempty"`
	// Level is the normalized log level (debug, info, warn, error,
	// fatal) when one could be extracted.
	Level string `json:"level,omitempty"`

	// Filled by template mining, empty until then.
	TemplateID string   `json:"template_id,omitempty"`
	Template   string   `json:"template,omitempty"`
	Parameters []string `json:"parameters,omitempty"`
}

// EffectiveMessage returns the parsed message when available, the raw
// line otherwise.
func (r *LogRecord) EffectiveMessage() string {
	if r.Message != "" {
		return r.Message
	}

	return r.Raw
}

type MetricSample struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}

// ChangeRecord describes a change applied to a system: a deployment, a
// configuration update, a restart... Changes are correlated with
// anomalies, never treated as anomalies themselves.
type ChangeRecord struct {
	Type    string `json:"type"`
	Version string `json:"version,omitempty"`
	Summary string `json:"summary,omitempty"`
}
