package detect

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

type Marking string

const (
	MarkingNormal      Marking = "normal"
	MarkingIgnore      Marking = "ignore"
	MarkingSymptomatic Marking = "symptomatic"
)

const (
	SignalLogNewTemplate     = "log.new_template"
	SignalLogRareTemplate    = "log.rare_template"
	SignalLogFrequencySpike  = "log.frequency_spike"
	SignalLogMissingTemplate = "log.missing_template"
	SignalLogSymptomatic     = "log.symptomatic_template"
)

type LogConfig struct {
	// LearningPeriod is the duration, from the first observation of a
	// source, during which new templates are considered normal.
	LearningPeriod time.Duration `yaml:"learning_period"`
	// RareThreshold is the cluster size at or below which a template is
	// considered rare.
	RareThreshold int64 `yaml:"rare_threshold"`
	// RareMinObservations is the number of logs a source must have
	// produced before rare templates are signaled.
	RareMinObservations int64 `yaml:"rare_min_observations"`
	// SpikeBucket is the width of the frequency buckets.
	SpikeBucket time.Duration `yaml:"spike_bucket"`
	// SpikeFactor is the increase over the learned per-bucket baseline
	// that qualifies as a spike.
	SpikeFactor float64 `yaml:"spike_factor"`
	// SpikeMinCount is the minimal bucket count for a spike.
	SpikeMinCount int64 `yaml:"spike_min_count"`
	// DisappearanceFactor: a template is missing when unseen for more
	// than this factor times its mean interval.
	DisappearanceFactor float64 `yaml:"disappearance_factor"`
	// DisappearanceMinCount is the minimal number of occurrences before
	// a template is expected to reappear.
	DisappearanceMinCount int64 `yaml:"disappearance_min_count"`
	// DisappearanceScanInterval bounds the frequency of the per-source
	// overdue templates scan.
	DisappearanceScanInterval time.Duration `yaml:"disappearance_scan_interval"`
	// Markings overrides the behavior of specific templates.
	Markings map[string]Marking `yaml:"markings"`
}

func DefaultLogConfig() *LogConfig {
	return &LogConfig{
		LearningPeriod:            5 * time.Minute,
		RareThreshold:             3,
		RareMinObservations:       500,
		SpikeBucket:               time.Minute,
		SpikeFactor:               3,
		SpikeMinCount:             10,
		DisappearanceFactor:       3,
		DisappearanceMinCount:     10,
		DisappearanceScanInterval: 30 * time.Second,
	}
}

// LogDetector produces signals from template-annotated log observations:
// new templates after the learning period, rare templates, frequency
// spikes, missing templates and explicitly marked templates.
type LogDetector struct {
	config *LogConfig

	mu      sync.Mutex
	sources map[string]*logSourceState
}

type logSourceState struct {
	FirstSeen time.Time                 `json:"first_seen"`
	Total     int64                     `json:"total"`
	Templates map[string]*templateStats `json:"templates"`
	LastScan  time.Time                 `json:"last_scan"`
}

type templateStats struct {
	Template       string    `json:"template"`
	Count          int64     `json:"count"`
	LastSeen       time.Time `json:"last_seen"`
	MeanIntervalS  float64   `json:"mean_interval_s"`
	BucketStart    time.Time `json:"bucket_start"`
	BucketCount    int64     `json:"bucket_count"`
	BucketBaseline float64   `json:"bucket_baseline"`
	SpikeSignaled  bool      `json:"spike_signaled"`
	MissingSignaled bool     `json:"missing_signaled"`
}

const intervalAlpha = 0.3

func NewLogDetector(config *LogConfig) *LogDetector {
	if config == nil {
		config = DefaultLogConfig()
	}

	return &LogDetector{
		config:  config,
		sources: map[string]*logSourceState{},
	}
}

func (d *LogDetector) Name() string {
	return "log"
}

func (d *LogDetector) Detect(obs *model.Observation) []model.Signal {
	if obs.Modality != model.ModalityLog || obs.Log == nil || obs.Log.TemplateID == "" {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	timestamp := obs.Timestamp

	state, exists := d.sources[obs.Source]
	if !exists {
		state = &logSourceState{
			FirstSeen: timestamp,
			Templates: map[string]*templateStats{},
		}
		d.sources[obs.Source] = state
	}

	state.Total++

	stats, exists := state.Templates[obs.Log.TemplateID]
	if !exists {
		stats = &templateStats{
			LastSeen:    timestamp,
			BucketStart: timestamp.Truncate(d.config.SpikeBucket),
		}
		state.Templates[obs.Log.TemplateID] = stats
	}

	stats.Template = obs.Log.Template

	if interval := timestamp.Sub(stats.LastSeen).Seconds(); stats.Count > 0 && interval >= 0 {
		if stats.MeanIntervalS == 0 {
			stats.MeanIntervalS = interval
		} else {
			stats.MeanIntervalS += intervalAlpha * (interval - stats.MeanIntervalS)
		}
	}

	stats.Count++
	stats.LastSeen = timestamp
	stats.MissingSignaled = false

	d.rollBucket(stats, timestamp)
	stats.BucketCount++

	learning := timestamp.Sub(state.FirstSeen) < d.config.LearningPeriod
	changeType := obs.Attributes[model.AttrTemplateChangeType]

	marking := d.config.Markings[obs.Log.Template]
	if marking == MarkingIgnore || marking == MarkingNormal {
		return nil
	}

	signals := []model.Signal{}

	newSignal := func(kind string, score float64, summary string, attributes map[string]string) model.Signal {
		if attributes == nil {
			attributes = map[string]string{}
		}

		attributes["template_id"] = obs.Log.TemplateID
		attributes["template"] = obs.Log.Template
		attributes["observation_id"] = obs.ID

		return model.Signal{
			Kind:       kind,
			Modality:   model.ModalityLog,
			Source:     obs.Source,
			Timestamp:  timestamp,
			Score:      score,
			Summary:    summary,
			Attributes: attributes,
		}
	}

	if marking == MarkingSymptomatic {
		signals = append(signals, newSignal(SignalLogSymptomatic, 0.9,
			fmt.Sprintf("symptomatic template observed: %s", obs.Log.Template), nil))
	}

	if !learning && changeType == "cluster_created" {
		signals = append(signals, newSignal(SignalLogNewTemplate, 0.8,
			fmt.Sprintf("new log template after learning period: %s", obs.Log.Template), nil))
	} else if !learning && stats.Count <= d.config.RareThreshold && state.Total >= d.config.RareMinObservations {
		signals = append(signals, newSignal(SignalLogRareTemplate, 0.6,
			fmt.Sprintf("rare log template (%d/%d observations): %s", stats.Count, state.Total, obs.Log.Template),
			map[string]string{
				"count": strconv.FormatInt(stats.Count, 10),
				"total": strconv.FormatInt(state.Total, 10),
			}))
	}

	spikeThreshold := max(float64(d.config.SpikeMinCount), stats.BucketBaseline*d.config.SpikeFactor)
	if !learning && !stats.SpikeSignaled && stats.BucketBaseline > 0 && float64(stats.BucketCount) >= spikeThreshold {
		stats.SpikeSignaled = true

		signals = append(signals, newSignal(SignalLogFrequencySpike, 0.7,
			fmt.Sprintf("frequency spike for template (%d in current bucket, baseline %.1f): %s",
				stats.BucketCount, stats.BucketBaseline, obs.Log.Template),
			map[string]string{
				"bucket_count": strconv.FormatInt(stats.BucketCount, 10),
				"baseline":     strconv.FormatFloat(stats.BucketBaseline, 'f', 2, 64),
			}))
	}

	signals = append(signals, d.scanMissing(state, timestamp, obs.Source)...)

	return signals
}

func (d *LogDetector) rollBucket(stats *templateStats, timestamp time.Time) {
	bucketStart := timestamp.Truncate(d.config.SpikeBucket)
	if !bucketStart.After(stats.BucketStart) {
		return
	}

	// Fold the finished bucket, then decay through any empty buckets.
	elapsed := bucketStart.Sub(stats.BucketStart) / d.config.SpikeBucket

	const bucketAlpha = 0.3

	stats.BucketBaseline += bucketAlpha * (float64(stats.BucketCount) - stats.BucketBaseline)
	for i := int64(1); i < int64(elapsed) && i <= 60; i++ {
		stats.BucketBaseline += bucketAlpha * (0 - stats.BucketBaseline)
	}

	stats.BucketStart = bucketStart
	stats.BucketCount = 0
	stats.SpikeSignaled = false
}

func (d *LogDetector) scanMissing(state *logSourceState, timestamp time.Time, source string) []model.Signal {
	if d.config.DisappearanceFactor <= 0 {
		return nil
	}

	if timestamp.Sub(state.LastScan) < d.config.DisappearanceScanInterval {
		return nil
	}

	state.LastScan = timestamp

	signals := []model.Signal{}

	for templateID, stats := range state.Templates {
		if stats.MissingSignaled || stats.Count < d.config.DisappearanceMinCount || stats.MeanIntervalS <= 0 {
			continue
		}

		if marking := d.config.Markings[stats.Template]; marking == MarkingIgnore || marking == MarkingNormal {
			continue
		}

		overdue := d.config.DisappearanceFactor * stats.MeanIntervalS
		silence := timestamp.Sub(stats.LastSeen).Seconds()

		if silence > overdue && silence > d.config.DisappearanceScanInterval.Seconds() {
			stats.MissingSignaled = true

			signals = append(signals, model.Signal{
				Kind:      SignalLogMissingTemplate,
				Modality:  model.ModalityLog,
				Source:    source,
				Timestamp: timestamp,
				Score:     0.6,
				Summary:   fmt.Sprintf("expected log template not seen for %.0fs (mean interval %.1fs): %s", silence, stats.MeanIntervalS, stats.Template),
				Attributes: map[string]string{
					"template_id":     templateID,
					"template":        stats.Template,
					"silence_s":       strconv.FormatFloat(silence, 'f', 0, 64),
					"mean_interval_s": strconv.FormatFloat(stats.MeanIntervalS, 'f', 1, 64),
				},
			})
		}
	}

	return signals
}

func (d *LogDetector) SnapshotKey() string {
	return "detect-log"
}

func (d *LogDetector) Snapshot() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	data, err := json.Marshal(d.sources)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return data, nil
}

func (d *LogDetector) Restore(data []byte) error {
	sources := map[string]*logSourceState{}
	if err := json.Unmarshal(data, &sources); err != nil {
		return errors.WithStack(err)
	}

	for _, state := range sources {
		if state.Templates == nil {
			state.Templates = map[string]*templateStats{}
		}
	}

	d.mu.Lock()
	d.sources = sources
	d.mu.Unlock()

	return nil
}
