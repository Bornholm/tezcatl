package detect

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bornholm/tezcatl/internal/core/humanize"
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
	// DisappearanceMaxCV is the largest coefficient of variation of the
	// intervals (standard deviation over mean) a template may have and
	// still be expected back. Past it the template is bursty, and its
	// mean interval predicts nothing. 0 or less expects every template
	// back, however irregular.
	DisappearanceMaxCV float64 `yaml:"disappearance_max_cv"`
	// MinTemplateLiterals is how much literal text a template must
	// keep, in letters and digits outside the placeholders, for its
	// shape to be worth reporting. 0 or less reports every template.
	MinTemplateLiterals int `yaml:"min_template_literals"`
	// Seasonality is "hourly" to learn hour-of-day baselines (crons,
	// nightly jobs, daily traffic patterns) or "none" for flat
	// baselines.
	Seasonality string `yaml:"seasonality"`
	// SeasonalMinObservations is the number of observations a source
	// must have accumulated at a given hour of day before that hour is
	// considered known (and templates never seen then are not expected).
	SeasonalMinObservations int64 `yaml:"seasonal_min_observations"`
	// Markings overrides the behavior of specific templates.
	Markings map[string]Marking `yaml:"markings"`
	// MaxTemplates caps the per-source template statistics; past it,
	// the template seen longest ago is dropped. The cap is deliberately
	// generous: an evicted template seen again counts as new, and may
	// signal once it should not. 0 removes the cap, at the price of
	// memory growing with template churn.
	MaxTemplates int `yaml:"max_templates"`
}

// DefaultMaxTemplates is an order of magnitude above what a service
// with healthy logging produces (the dogfooding instance tops out
// around 60 templates per source), so eviction only bites on template
// churn: identifiers leaking into messages faster than masking catches
// them.
const DefaultMaxTemplates = 2000

// DefaultDisappearanceMaxCV separates a heartbeat from a burst. Fully
// random arrivals (a Poisson process: web requests, SSH scans) have a
// coefficient of variation of 1, and bursty traffic goes well above it;
// something emitted on a timer stays near 0. Half of the Poisson value
// keeps the timers and drops the rest.
//
// The number answers what the dogfooding instance kept reporting: 118
// disappearance signals in seven hours, of which the loudest read
// "expected log template not seen for 5623s (mean interval 6.0s)" —
// nginx access logs of a blog nobody was reading. A mean interval only
// predicts the next occurrence when the intervals cluster around it.
const DefaultDisappearanceMaxCV = 0.5

// DefaultMinTemplateLiterals is the floor a template must clear to be
// worth naming as a shape. An access log mined down to
// `<IP> - - <*> +<NUM>] <*> <*> HTTP/<NUM>.<NUM>" <NUM> <NUM>` keeps
// four letters, "HTTP", and matches every HTTP request ever served:
// saying that shape is new, rare or frequent says nothing. A real
// message keeps whole words.
const DefaultMinTemplateLiterals = 8

const (
	SeasonalityNone   = "none"
	SeasonalityHourly = "hourly"
)

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
		DisappearanceMaxCV:        DefaultDisappearanceMaxCV,
		Seasonality:               SeasonalityHourly,
		SeasonalMinObservations:   50,
		MaxTemplates:              DefaultMaxTemplates,
	}
}

// LogDetector produces signals from template-annotated log observations:
// new templates after the learning period, rare templates, frequency
// spikes, missing templates and explicitly marked templates.
//
// Markings are dynamic: they are seeded from the configuration, can be
// updated at runtime (SetMarking) and are persisted with the detector
// state.
type LogDetector struct {
	config *LogConfig

	mu       sync.Mutex
	markings map[string]Marking
	sources  map[string]*logSourceState
}

// makeRoom drops the template statistics seen longest ago when the cap
// is reached, mirroring the metric detector's series cap: template
// churn (identifiers escaping the masking) must not grow the state
// without bound. The caller holds the detector lock.
func (s *logSourceState) makeRoom(maxTemplates int) {
	if maxTemplates <= 0 || len(s.Templates) < maxTemplates {
		return
	}

	for len(s.Templates) >= maxTemplates {
		var (
			oldestID   string
			oldestSeen time.Time
		)

		for id, stats := range s.Templates {
			if oldestID == "" || stats.LastSeen.Before(oldestSeen) {
				oldestID, oldestSeen = id, stats.LastSeen
			}
		}

		delete(s.Templates, oldestID)
	}
}

type logSourceState struct {
	FirstSeen time.Time                 `json:"first_seen"`
	Total     int64                     `json:"total"`
	Templates map[string]*templateStats `json:"templates"`
	LastScan  time.Time                 `json:"last_scan"`
	// HourlySeen counts the observations of the source per hour of day,
	// telling seasonality which hours are known to be active.
	HourlySeen [24]int64 `json:"hourly_seen,omitempty"`
}

type templateStats struct {
	Template      string    `json:"template"`
	Count         int64     `json:"count"`
	LastSeen      time.Time `json:"last_seen"`
	MeanIntervalS float64   `json:"mean_interval_s"`
	// IntervalVarianceS2 is the EWMA variance of the intervals, in
	// square seconds. Compared to the mean it tells a heartbeat from a
	// burst, which is what makes a missing template worth reporting.
	IntervalVarianceS2 float64 `json:"interval_variance_s2,omitempty"`
	// IntervalSamples counts the intervals folded into the variance,
	// which is not the occurrence count: a state file restored from a
	// version that did not track the variance carries occurrences by
	// the thousand and no dispersion at all.
	IntervalSamples int64     `json:"interval_samples,omitempty"`
	BucketStart     time.Time `json:"bucket_start"`
	BucketCount     int64     `json:"bucket_count"`
	BucketBaseline  float64   `json:"bucket_baseline"`
	SpikeSignaled   bool      `json:"spike_signaled"`
	MissingSignaled bool      `json:"missing_signaled"`
	// HourlyCounts counts the occurrences of the template per hour of
	// day; HourlyBaseline is the per-bucket EWMA per hour of day, and
	// HourlyFolded the number of buckets folded into it.
	HourlyCounts   [24]int64   `json:"hourly_counts,omitempty"`
	HourlyBaseline [24]float64 `json:"hourly_baseline,omitempty"`
	HourlyFolded   [24]int64   `json:"hourly_folded,omitempty"`
}

const intervalAlpha = 0.3

// minIntervalSamples is how many intervals the EWMA variance needs
// before it says anything. With intervalAlpha at 0.3 the estimate is
// carried by roughly the last half-dozen intervals, so five is where it
// stops being an artifact of its own initialization.
const minIntervalSamples = 5

// intervalCV is the coefficient of variation of the intervals: 0 for a
// perfect metronome, 1 for fully random arrivals, more for bursts. The
// second return value is false while too few intervals have been folded
// in to tell one from the other.
func (s *templateStats) intervalCV() (float64, bool) {
	if s.MeanIntervalS <= 0 || s.IntervalSamples < minIntervalSamples {
		return 0, false
	}

	return math.Sqrt(s.IntervalVarianceS2) / s.MeanIntervalS, true
}

func NewLogDetector(config *LogConfig) *LogDetector {
	if config == nil {
		config = DefaultLogConfig()
	}

	markings := map[string]Marking{}
	maps.Copy(markings, config.Markings)

	return &LogDetector{
		config:   config,
		markings: markings,
		sources:  map[string]*logSourceState{},
	}
}

// ValidMarking reports whether a marking value is supported.
func ValidMarking(marking Marking) bool {
	switch marking {
	case MarkingNormal, MarkingIgnore, MarkingSymptomatic:
		return true
	default:
		return false
	}
}

// SetMarking overrides the behavior of a template at runtime. An empty
// marking clears the override; note that a marking also seeded by the
// configuration reappears at the next restart.
func (d *LogDetector) SetMarking(template string, marking Marking) error {
	if marking != "" && !ValidMarking(marking) {
		return errors.Errorf("unsupported marking %q", marking)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if marking == "" {
		delete(d.markings, template)
		return nil
	}

	d.markings[template] = marking

	return nil
}

// Markings returns a copy of the current markings.
func (d *LogDetector) Markings() map[string]Marking {
	d.mu.Lock()
	defer d.mu.Unlock()

	markings := make(map[string]Marking, len(d.markings))
	maps.Copy(markings, d.markings)

	return markings
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
		state.makeRoom(d.config.MaxTemplates)

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
			delta := interval - stats.MeanIntervalS
			stats.MeanIntervalS += intervalAlpha * delta
			stats.IntervalVarianceS2 = (1 - intervalAlpha) *
				(stats.IntervalVarianceS2 + intervalAlpha*delta*delta)
			stats.IntervalSamples++
		}
	}

	stats.Count++
	stats.LastSeen = timestamp
	stats.MissingSignaled = false

	if d.seasonal() {
		hour := timestamp.Hour()
		state.HourlySeen[hour]++
		stats.HourlyCounts[hour]++
	}

	d.rollBucket(stats, timestamp)
	stats.BucketCount++

	learning := timestamp.Sub(state.FirstSeen) < d.config.LearningPeriod
	changeType := obs.Attributes[model.AttrTemplateChangeType]

	marking := d.markings[obs.Log.Template]
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

	// A template made almost entirely of placeholders matches
	// everything, so its shape being new, rare or unusually frequent
	// is not information. Its silence still is: "this stopped" is
	// about volume, not shape, and stays worth saying.
	informative := d.informative(obs.Log.Template)

	if !learning && informative && changeType == "cluster_created" {
		signals = append(signals, newSignal(SignalLogNewTemplate, 0.8,
			fmt.Sprintf("new log template after learning period: %s", obs.Log.Template), nil))
	} else if !learning && informative && stats.Count <= d.config.RareThreshold && state.Total >= d.config.RareMinObservations {
		signals = append(signals, newSignal(SignalLogRareTemplate, 0.6,
			fmt.Sprintf("rare log template (%d/%d observations): %s", stats.Count, state.Total, obs.Log.Template),
			map[string]string{
				"count": strconv.FormatInt(stats.Count, 10),
				"total": strconv.FormatInt(state.Total, 10),
			}))
	}

	baseline := d.spikeBaseline(stats, timestamp)

	spikeThreshold := max(float64(d.config.SpikeMinCount), baseline*d.config.SpikeFactor)
	if !learning && informative && !stats.SpikeSignaled && baseline > 0 && float64(stats.BucketCount) >= spikeThreshold {
		stats.SpikeSignaled = true

		signals = append(signals, newSignal(SignalLogFrequencySpike, 0.7,
			fmt.Sprintf("frequency spike for template (%d in current bucket, baseline %.1f): %s",
				stats.BucketCount, baseline, obs.Log.Template),
			map[string]string{
				"bucket_count": strconv.FormatInt(stats.BucketCount, 10),
				"baseline":     strconv.FormatFloat(baseline, 'f', 2, 64),
			}))
	}

	signals = append(signals, d.scanMissing(state, timestamp, obs.Source)...)

	return signals
}

// informative reports whether a template says enough to be worth
// reporting as a shape. It counts the letters and digits left once the
// placeholders are removed: a template that keeps whole words
// describes a message, one that keeps punctuation describes a format.
func (d *LogDetector) informative(template string) bool {
	floor := d.config.MinTemplateLiterals
	if floor <= 0 {
		return true
	}

	return templateLiterals(template) >= floor
}

// templateLiterals counts the alphanumeric characters outside the
// placeholders. Exported behaviour, unexported name: the threshold is
// what an operator tunes, not the counting.
func templateLiterals(template string) int {
	literals := 0

	for i := 0; i < len(template); i++ {
		if template[i] == '<' {
			// Skip the placeholder, if this is one: <NUM>, <IP>, <*>...
			if end := strings.IndexByte(template[i:], '>'); end > 0 && end <= maxPlaceholderLength {
				i += end

				continue
			}
		}

		switch c := template[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			literals++
		}
	}

	return literals
}

// maxPlaceholderLength bounds what counts as a placeholder, so a line
// of shell redirections is not read as one long mask.
const maxPlaceholderLength = 8

func (d *LogDetector) seasonal() bool {
	return d.config.Seasonality == SeasonalityHourly
}

// spikeBaseline picks the expected per-bucket count: the hour-of-day
// baseline when seasonality has enough history for the current hour, the
// flat baseline otherwise.
func (d *LogDetector) spikeBaseline(stats *templateStats, timestamp time.Time) float64 {
	if !d.seasonal() {
		return stats.BucketBaseline
	}

	const minFoldedBuckets = 3

	hour := timestamp.Hour()
	if stats.HourlyFolded[hour] >= minFoldedBuckets {
		return stats.HourlyBaseline[hour]
	}

	return stats.BucketBaseline
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

	// The hour-of-day baseline only folds observed buckets, so a quiet
	// night does not erase what a busy hour looks like.
	if d.seasonal() {
		hour := stats.BucketStart.Hour()
		stats.HourlyBaseline[hour] += bucketAlpha * (float64(stats.BucketCount) - stats.HourlyBaseline[hour])
		stats.HourlyFolded[hour]++
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

		if marking := d.markings[stats.Template]; marking == MarkingIgnore || marking == MarkingNormal {
			continue
		}

		// Seasonality: when the source is known to be active at this
		// hour of day but the template has never appeared then (a
		// nightly cron checked at noon), do not expect it.
		if d.seasonal() {
			hour := timestamp.Hour()
			if stats.HourlyCounts[hour] == 0 && state.HourlySeen[hour] >= d.config.SeasonalMinObservations {
				continue
			}
		}

		// Regularity: a mean interval only predicts the next occurrence
		// when the intervals cluster around it. A template arriving in
		// bursts is silent all the time, and its silence says nothing.
		// Until the dispersion is known the template is not expected
		// back either: an unmeasured template is not a regular one.
		cv, measured := stats.intervalCV()
		if maxCV := d.config.DisappearanceMaxCV; maxCV > 0 && (!measured || cv > maxCV) {
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
				// The prose is humanized, the attributes below keep raw
				// seconds: "1h 40m" is for the reader, "6002" is for
				// whatever parses the event.
				Summary: fmt.Sprintf("expected log template not seen for %s (mean interval %s): %s",
					humanize.Seconds(silence), humanize.Seconds(stats.MeanIntervalS), stats.Template),
				Attributes: map[string]string{
					"template_id":     templateID,
					"template":        stats.Template,
					"silence_s":       strconv.FormatFloat(silence, 'f', 0, 64),
					"mean_interval_s": strconv.FormatFloat(stats.MeanIntervalS, 'f', 1, 64),
					// The dispersion of the intervals, so a reader can
					// judge how much the mean was worth predicting.
					"interval_cv": strconv.FormatFloat(cv, 'f', 2, 64),
				},
			})
		}
	}

	return signals
}

func (d *LogDetector) SnapshotKey() string {
	return "detect-log"
}

type logSnapshot struct {
	Sources  map[string]*logSourceState `json:"sources"`
	Markings map[string]Marking         `json:"markings,omitempty"`
}

func (d *LogDetector) Snapshot() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	data, err := json.Marshal(logSnapshot{
		Sources:  d.sources,
		Markings: d.markings,
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return data, nil
}

func (d *LogDetector) Restore(data []byte) error {
	var snapshot logSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Sources == nil {
		// Legacy format: the sources map alone.
		sources := map[string]*logSourceState{}
		if legacyErr := json.Unmarshal(data, &sources); legacyErr != nil {
			return errors.WithStack(legacyErr)
		}

		snapshot = logSnapshot{Sources: sources}
	}

	for _, state := range snapshot.Sources {
		if state.Templates == nil {
			state.Templates = map[string]*templateStats{}
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.sources = snapshot.Sources

	// Configuration markings are the seed, persisted runtime markings
	// override them.
	markings := map[string]Marking{}
	maps.Copy(markings, d.config.Markings)
	maps.Copy(markings, snapshot.Markings)
	d.markings = markings

	return nil
}
