package model

import "time"

type Modality string

const (
	ModalityLog    Modality = "log"
	ModalityMetric Modality = "metric"
	ModalityTrace  Modality = "trace"
)

// Observation is the normalized representation of any ingested datum,
// whatever its modality. Exactly one of the modality-specific payloads
// (Log, Metric) is expected to be non-nil, matching Modality.
type Observation struct {
	ID         string            `json:"id"`
	Source     string            `json:"source"`
	Modality   Modality          `json:"modality"`
	Timestamp  time.Time         `json:"timestamp"`
	IngestedAt time.Time         `json:"ingested_at"`
	Attributes map[string]string `json:"attributes,omitempty"`

	Log    *LogRecord    `json:"log,omitempty"`
	Metric *MetricSample `json:"metric,omitempty"`
}

// PartitionKey identifies the sequential processing unit an observation
// belongs to: observations sharing a key are always handled by the same
// engine worker, in ingestion order. Partitioning by source keeps all
// modalities of a system on the same worker, which gives correlation a
// consistent view without cross-worker synchronization.
func (o *Observation) PartitionKey() string {
	return o.Source
}

type LogRecord struct {
	Raw string `json:"raw"`

	// Filled by template mining, empty until then.
	TemplateID string   `json:"template_id,omitempty"`
	Template   string   `json:"template,omitempty"`
	Parameters []string `json:"parameters,omitempty"`
}

type MetricSample struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}
