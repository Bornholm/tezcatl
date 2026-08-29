package detect

import (
	"encoding/json"
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

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
	// ("*.percent"); an exact match wins, otherwise the largest
	// matching floor applies.
	MinDeltas map[string]float64 `yaml:"min_deltas"`
}

func DefaultMetricConfig() *MetricConfig {
	return &MetricConfig{
		WarmupSamples:  30,
		Alpha:          0.05,
		ZThreshold:     3,
		TrendFastAlpha: 0.3,
		TrendSlowAlpha: 0.05,
		TrendThreshold: 0.5,
		MinDeltas:      DefaultMinDeltas(),
	}
}

// DefaultMinDeltas covers the metric families tezcatl itself produces
// (host and docker collectors): below these absolute deviations, a
// statistically significant move is still operationally meaningless.
func DefaultMinDeltas() map[string]float64 {
	return map[string]float64{
		"*.percent":    1,
		"system.load1": 0.5,
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

	mu     sync.Mutex
	series map[string]*metricStats
}

type metricStats struct {
	Count         int64   `json:"count"`
	Mean          float64 `json:"mean"`
	Variance      float64 `json:"variance"`
	FastEWMA      float64 `json:"fast_ewma"`
	SlowEWMA      float64 `json:"slow_ewma"`
	DriftSignaled bool    `json:"drift_signaled"`
}

func NewMetricDetector(config *MetricConfig) *MetricDetector {
	if config == nil {
		config = DefaultMetricConfig()
	}

	return &MetricDetector{
		config: config,
		series: map[string]*metricStats{},
	}
}

func (d *MetricDetector) Name() string {
	return "metric"
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
		stats = &metricStats{}
		d.series[key] = stats
	}

	value := obs.Metric.Value

	signals := []model.Signal{}

	newSignal := func(kind string, score float64, summary string, attributes map[string]string) model.Signal {
		if attributes == nil {
			attributes = map[string]string{}
		}

		attributes["metric"] = obs.Metric.Name
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
				fmt.Sprintf("%s = %g above configured maximum %g", obs.Metric.Name, value, *rule.Max),
				map[string]string{"max": strconv.FormatFloat(*rule.Max, 'f', -1, 64)}))
		}

		if rule.Min != nil && value < *rule.Min {
			signals = append(signals, newSignal(SignalMetricThreshold, 0.9,
				fmt.Sprintf("%s = %g below configured minimum %g", obs.Metric.Name, value, *rule.Min),
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
					fmt.Sprintf("%s = %g deviates from baseline %.3g (z = %.1f)", obs.Metric.Name, value, stats.Mean, z),
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
		})
	}

	sort.Slice(series, func(i, j int) bool { return series[i].Key < series[j].Key })

	return series
}

func (d *MetricDetector) SnapshotKey() string {
	return "detect-metric"
}

func (d *MetricDetector) Snapshot() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	data, err := json.Marshal(d.series)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return data, nil
}

func (d *MetricDetector) Restore(data []byte) error {
	series := map[string]*metricStats{}
	if err := json.Unmarshal(data, &series); err != nil {
		return errors.WithStack(err)
	}

	d.mu.Lock()
	d.series = series
	d.mu.Unlock()

	return nil
}
