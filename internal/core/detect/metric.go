package detect

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

const (
	SignalMetricZScore     = "metric.zscore"
	SignalMetricThreshold  = "metric.threshold"
	SignalMetricTrendDrift = "metric.trend_drift"
)

type ThresholdRule struct {
	Metric string   `yaml:"metric"`
	Min    *float64 `yaml:"min"`
	Max    *float64 `yaml:"max"`
}

type MetricConfig struct {
	// WarmupSamples is the number of samples a series needs before
	// statistical signals are produced.
	WarmupSamples int64 `yaml:"warmup_samples"`
	// Alpha is the EWMA smoothing factor for the mean/variance baseline.
	Alpha float64 `yaml:"alpha"`
	// ZThreshold is the absolute z-score above which a sample is
	// anomalous.
	ZThreshold float64 `yaml:"z_threshold"`
	// TrendFastAlpha and TrendSlowAlpha smooth the two EWMAs compared
	// for drift detection.
	TrendFastAlpha float64 `yaml:"trend_fast_alpha"`
	TrendSlowAlpha float64 `yaml:"trend_slow_alpha"`
	// TrendThreshold is the relative divergence between the fast and
	// slow EWMAs that qualifies as a drift.
	TrendThreshold float64 `yaml:"trend_threshold"`
	// Thresholds are static per-metric bounds.
	Thresholds []ThresholdRule `yaml:"thresholds"`
	// MinDeltas floors the statistical signals: no z-score or drift is
	// produced when the absolute deviation from the baseline is below
	// the floor. Near-constant series (an idle container's CPU) have a
	// tiny variance, so trivial fluctuations otherwise score huge
	// z-values. Keys are metric names or path.Match globs
	// ("*percent"); an exact match wins, otherwise the largest
	// matching floor applies. Beware that path.Match gives "." no
	// special meaning: "*.percent" misses "memory.used_percent".
	MinDeltas map[string]float64 `yaml:"min_deltas"`
	// MaxSeries caps how many series a detector keeps. Sources create
	// series without ever retiring them: a container name, a pod name
	// or a request path that carries an identifier all mint a key that
	// is written once and never fed again. Past the cap, the series
	// seen longest ago is dropped to make room. 0 removes the cap, at
	// the price of memory growing with the churn of the environment.
	MaxSeries int `yaml:"max_series"`
}

// DefaultMaxSeries is high enough to hold every real series of a busy
// cluster (thousands of pods times a handful of metrics) and low enough
// that a runaway source hits a wall instead of the machine's memory.
const DefaultMaxSeries = 10000

func DefaultMetricConfig() *MetricConfig {
	return &MetricConfig{
		WarmupSamples:  30,
		Alpha:          0.05,
		ZThreshold:     3,
		TrendFastAlpha: 0.3,
		TrendSlowAlpha: 0.05,
		TrendThreshold: 0.5,
		MinDeltas:      DefaultMinDeltas(),
		MaxSeries:      DefaultMaxSeries,
	}
}

// DefaultMinDeltas floors the one unit whose scale is known without
// knowing the metric: a percentage runs from 0 to 100 whoever emits it.
// Any other unit needs a floor an operator sets, since the core cannot
// guess the scale of a name it has never seen.
//
// Five points comes from measurement, not taste: on the dogfooding
// instance, 27 of the 34 percentage z-scores of a quiet night moved
// less than that, the smallest of them barely above one point (a host
// CPU going from 0.87% to 1.91%, reported critical). Everything a
// person would act on cleared it.
func DefaultMinDeltas() map[string]float64 {
	return map[string]float64{
		// Match the suffix, not a separator: "cpu.percent" and
		// "memory.used_percent" are both percentages.
		"*percent": 5,
	}
}

// minDelta resolves the significance floor of one metric.
func (c *MetricConfig) minDelta(metric string) float64 {
	if floor, exists := c.MinDeltas[metric]; exists {
		return floor
	}

	max := 0.0
	for pattern, floor := range c.MinDeltas {
		if matched, err := path.Match(pattern, metric); err == nil && matched && floor > max {
			max = floor
		}
	}

	return max
}

// MetricDetector produces signals from metric samples using simple,
// explainable statistics: EWMA mean/variance z-scores, static thresholds
// and fast/slow EWMA trend drift.
type MetricDetector struct {
	config *MetricConfig

	mu      sync.Mutex
	series  map[string]*metricStats
	evicted int64
	// ignored silences the statistical signals of matching series at
	// runtime, the metric side of the template marking loop. Keys are
	// exact series keys ("source/metric{labels}"), exact metric names,
	// or path.Match globs over either. Baselines keep learning while
	// ignored, so clearing the mark resumes cleanly.
	ignored map[string]bool
}

type metricStats struct {
	Count         int64   `json:"count"`
	Mean          float64 `json:"mean"`
	Variance      float64 `json:"variance"`
	FastEWMA      float64 `json:"fast_ewma"`
	SlowEWMA      float64 `json:"slow_ewma"`
	DriftSignaled bool    `json:"drift_signaled"`
	// LastSeen is the timestamp of the latest sample, used to pick
	// what to drop when the series cap is reached. It is absent from
	// snapshots written before the cap existed, which leaves those
	// series first in line: they are the stale ones anyway.
	LastSeen time.Time `json:"last_seen,omitzero"`
}

func NewMetricDetector(config *MetricConfig) *MetricDetector {
	if config == nil {
		config = DefaultMetricConfig()
	}

	return &MetricDetector{
		config:  config,
		series:  map[string]*metricStats{},
		ignored: map[string]bool{},
	}
}

// SetIgnored silences (or, with ignore false, restores) the statistical
// signals of the series matching pattern: an exact series key, an exact
// metric name, or a path.Match glob over either.
func (d *MetricDetector) SetIgnored(pattern string, ignore bool) error {
	if pattern == "" {
		return errors.New("missing metric pattern")
	}

	if _, err := path.Match(pattern, "x"); err != nil {
		return errors.Wrapf(err, "malformed pattern %q", pattern)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if ignore {
		d.ignored[pattern] = true
	} else {
		delete(d.ignored, pattern)
	}

	return nil
}

// isIgnored reports whether a series is silenced. The caller holds the
// lock.
func (d *MetricDetector) isIgnored(key string, metric string) bool {
	if d.ignored[key] || d.ignored[metric] {
		return true
	}

	for pattern := range d.ignored {
		if matched, err := path.Match(pattern, key); err == nil && matched {
			return true
		}

		if matched, err := path.Match(pattern, metric); err == nil && matched {
			return true
		}
	}

	return false
}

func (d *MetricDetector) Name() string {
	return "metric"
}

// makeRoom drops the least recently seen series when the cap is
// reached, so a source that keeps minting new keys cannot grow the
// state without bound. The caller holds the lock.
func (d *MetricDetector) makeRoom() {
	if d.config.MaxSeries <= 0 || len(d.series) < d.config.MaxSeries {
		return
	}

	// Dropping down to the cap covers the case of a restored snapshot
	// larger than a cap that has since been lowered.
	for len(d.series) >= d.config.MaxSeries {
		var (
			oldestKey  string
			oldestSeen time.Time
		)

		for key, stats := range d.series {
			if oldestKey == "" || stats.LastSeen.Before(oldestSeen) {
				oldestKey, oldestSeen = key, stats.LastSeen
			}
		}

		delete(d.series, oldestKey)

		d.evicted++
	}

	// One line per power of ten: enough to notice saturation, quiet
	// enough to live with a permanently churning environment.
	if isPowerOfTen(d.evicted) {
		slog.Warn("metric series cap reached, dropping the least recently seen series",
			slog.Int("max_series", d.config.MaxSeries),
			slog.Int64("evicted_total", d.evicted))
	}
}

func isPowerOfTen(value int64) bool {
	for value >= 10 && value%10 == 0 {
		value /= 10
	}

	return value == 1
}

func (d *MetricDetector) Detect(obs *model.Observation) []model.Signal {
	if obs.Modality != model.ModalityMetric || obs.Metric == nil {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	key := seriesKey(obs.Source, obs.Metric)

	stats, exists := d.series[key]
	if !exists {
		d.makeRoom()

		stats = &metricStats{}
		d.series[key] = stats
	}

	stats.LastSeen = obs.Timestamp

	value := obs.Metric.Value

	// An ignored series keeps learning its baseline silently, so
	// clearing the mark resumes detection without a warmup.
	if d.isIgnored(key, obs.Metric.Name) {
		d.update(stats, value)

		return nil
	}

	signals := []model.Signal{}

	newSignal := func(kind string, score float64, summary string, attributes map[string]string) model.Signal {
		if attributes == nil {
			attributes = map[string]string{}
		}

		attributes["metric"] = obs.Metric.Name
		// The full series key, labels included: the identity the
		// marking API takes, so a reader can silence this series and
		// not every series sharing the metric's name.
		attributes["series"] = key
		attributes["value"] = strconv.FormatFloat(value, 'f', -1, 64)
		attributes["observation_id"] = obs.ID

		return model.Signal{
			Kind:       kind,
			Modality:   model.ModalityMetric,
			Source:     obs.Source,
			Timestamp:  obs.Timestamp,
			Score:      score,
			Summary:    summary,
			Attributes: attributes,
		}
	}

	// Static thresholds apply from the first sample.
	for _, rule := range d.config.Thresholds {
		if rule.Metric != obs.Metric.Name {
			continue
		}

		if rule.Max != nil && value > *rule.Max {
			signals = append(signals, newSignal(SignalMetricThreshold, 0.9,
				fmt.Sprintf("%s = %.4g above configured maximum %g", obs.Metric.Name, value, *rule.Max),
				map[string]string{"max": strconv.FormatFloat(*rule.Max, 'f', -1, 64)}))
		}

		if rule.Min != nil && value < *rule.Min {
			signals = append(signals, newSignal(SignalMetricThreshold, 0.9,
				fmt.Sprintf("%s = %.4g below configured minimum %g", obs.Metric.Name, value, *rule.Min),
				map[string]string{"min": strconv.FormatFloat(*rule.Min, 'f', -1, 64)}))
		}
	}

	// Statistical signals compare the sample to the baseline learned
	// from previous samples, then fold it in.
	if stats.Count >= d.config.WarmupSamples {
		minDelta := d.config.minDelta(obs.Metric.Name)

		stddev := math.Sqrt(stats.Variance)
		if stddev > 0 && math.Abs(value-stats.Mean) >= minDelta {
			z := (value - stats.Mean) / stddev
			if math.Abs(z) >= d.config.ZThreshold {
				score := math.Min(0.95, 0.5+math.Abs(z)/10)

				signals = append(signals, newSignal(SignalMetricZScore, score,
					// Four significant digits: a summary is read, and
					// "30.585181299599963" carries no more meaning than
					// "30.59". The exact value stays in the attributes.
					fmt.Sprintf("%s = %.4g deviates from baseline %.3g (z = %.1f)", obs.Metric.Name, value, stats.Mean, z),
					map[string]string{
						"mean":   strconv.FormatFloat(stats.Mean, 'g', 4, 64),
						"stddev": strconv.FormatFloat(stddev, 'g', 4, 64),
						"z":      strconv.FormatFloat(z, 'f', 2, 64),
					}))
			}
		}

		drift := math.Abs(stats.FastEWMA-stats.SlowEWMA) / math.Max(math.Abs(stats.SlowEWMA), 1e-9)
		if drift >= d.config.TrendThreshold && math.Abs(stats.FastEWMA-stats.SlowEWMA) >= minDelta {
			if !stats.DriftSignaled {
				stats.DriftSignaled = true

				signals = append(signals, newSignal(SignalMetricTrendDrift, 0.6,
					fmt.Sprintf("%s drifting: fast trend %.3g vs baseline %.3g", obs.Metric.Name, stats.FastEWMA, stats.SlowEWMA),
					map[string]string{
						"fast": strconv.FormatFloat(stats.FastEWMA, 'g', 4, 64),
						"slow": strconv.FormatFloat(stats.SlowEWMA, 'g', 4, 64),
					}))
			}
		} else {
			stats.DriftSignaled = false
		}
	}

	d.update(stats, value)

	return signals
}

func (d *MetricDetector) update(stats *metricStats, value float64) {
	if stats.Count == 0 {
		stats.Mean = value
		stats.FastEWMA = value
		stats.SlowEWMA = value
		stats.Count++

		return
	}

	alpha := d.config.Alpha

	delta := value - stats.Mean
	stats.Mean += alpha * delta
	stats.Variance = (1 - alpha) * (stats.Variance + alpha*delta*delta)

	stats.FastEWMA += d.config.TrendFastAlpha * (value - stats.FastEWMA)
	stats.SlowEWMA += d.config.TrendSlowAlpha * (value - stats.SlowEWMA)

	stats.Count++
}

func seriesKey(source string, metric *model.MetricSample) string {
	if len(metric.Labels) == 0 {
		return source + "/" + metric.Name
	}

	keys := make([]string, 0, len(metric.Labels))
	for key := range metric.Labels {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(source)
	b.WriteString("/")
	b.WriteString(metric.Name)

	for _, key := range keys {
		b.WriteString("{")
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(metric.Labels[key])
		b.WriteString("}")
	}

	return b.String()
}

// IgnoredPatterns lists the runtime metric markings, sorted.
func (d *MetricDetector) IgnoredPatterns() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	patterns := make([]string, 0, len(d.ignored))
	for pattern := range d.ignored {
		patterns = append(patterns, pattern)
	}

	sort.Strings(patterns)

	return patterns
}

// SeriesInfo is the learned state of one metric series, for inspection.
type SeriesInfo struct {
	// Key is "source/metric{labels}".
	Key     string  `json:"key"`
	Samples int64   `json:"samples"`
	Mean    float64 `json:"mean"`
	StdDev  float64 `json:"std_dev"`
	// Recent is the fast EWMA: the level of the latest samples, to
	// compare with Mean at a glance.
	Recent float64 `json:"recent"`
	// Warmup is true while the series has too few samples for
	// statistical signals.
	Warmup bool `json:"warmup"`
	// Ignored is true when a runtime marking silences this series.
	Ignored bool `json:"ignored,omitempty"`
}

// Series lists the learned metric series, sorted by key.
func (d *MetricDetector) Series() []SeriesInfo {
	d.mu.Lock()
	defer d.mu.Unlock()

	series := make([]SeriesInfo, 0, len(d.series))

	for key, stats := range d.series {
		series = append(series, SeriesInfo{
			Key:     key,
			Samples: stats.Count,
			Mean:    stats.Mean,
			StdDev:  math.Sqrt(stats.Variance),
			Recent:  stats.FastEWMA,
			Warmup:  stats.Count < d.config.WarmupSamples,
			Ignored: d.isIgnored(key, metricNameOfKey(key)),
		})
	}

	sort.Slice(series, func(i, j int) bool { return series[i].Key < series[j].Key })

	return series
}

func (d *MetricDetector) SnapshotKey() string {
	return "detect-metric"
}

// metricSnapshot is the persisted form: the learned series plus the
// runtime markings, so an ignored series stays ignored across restarts.
type metricSnapshot struct {
	Series  map[string]*metricStats `json:"series"`
	Ignored []string                `json:"ignored,omitempty"`
}

func (d *MetricDetector) Snapshot() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	snapshot := metricSnapshot{Series: d.series}
	for pattern := range d.ignored {
		snapshot.Ignored = append(snapshot.Ignored, pattern)
	}

	sort.Strings(snapshot.Ignored)

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return data, nil
}

func (d *MetricDetector) Restore(data []byte) error {
	snapshot := metricSnapshot{}
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Series == nil {
		// Snapshots written before the markings existed are the bare
		// series map.
		series := map[string]*metricStats{}
		if err := json.Unmarshal(data, &series); err != nil {
			return errors.WithStack(err)
		}

		snapshot = metricSnapshot{Series: series}
	}

	ignored := make(map[string]bool, len(snapshot.Ignored))
	for _, pattern := range snapshot.Ignored {
		ignored[pattern] = true
	}

	d.mu.Lock()
	d.series = snapshot.Series
	d.ignored = ignored
	d.mu.Unlock()

	return nil
}

// metricNameOfKey extracts the metric name from a series key
// ("source/metric{labels}"): the last path segment, labels stripped.
func metricNameOfKey(key string) string {
	if brace := strings.IndexByte(key, '{'); brace >= 0 {
		key = key[:brace]
	}

	if slash := strings.LastIndexByte(key, '/'); slash >= 0 {
		key = key[slash+1:]
	}

	return key
}
