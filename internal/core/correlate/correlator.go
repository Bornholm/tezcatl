package correlate

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/window"
)

type Config struct {
	// Window is how long signals of a same source are aggregated into a
	// single event before it is emitted. It is also the upper bound on
	// the emission latency of an event.
	Window time.Duration `yaml:"window"`
	// ContextBefore/ContextAfter bound the number of observations
	// attached around the first signal.
	ContextBefore int `yaml:"context_before"`
	ContextAfter  int `yaml:"context_after"`
}

func DefaultConfig() *Config {
	return &Config{
		Window:        30 * time.Second,
		ContextBefore: 10,
		ContextAfter:  10,
	}
}

// Correlator aggregates the signals of a same source arriving within a
// time window into a single contextualized event: deduplicated signals, a
// combined confidence score and the observations surrounding the first
// signal. It is safe for concurrent use.
type Correlator struct {
	config *Config
	now    func() time.Time

	mu      sync.Mutex
	sources map[string]*sourceState
}

type sourceState struct {
	ring    *window.Ring
	pending *pendingEvent
}

type pendingEvent struct {
	firstReceivedAt time.Time
	firstSignalAt   time.Time
	signals         map[string]*aggregatedSignal
	before          []model.Observation
	after           []model.Observation
}

type aggregatedSignal struct {
	signal model.Signal
	count  int64
}

func NewCorrelator(config *Config) *Correlator {
	if config == nil {
		config = DefaultConfig()
	}

	return &Correlator{
		config:  config,
		now:     time.Now,
		sources: map[string]*sourceState{},
	}
}

// Observe records an observation for context: it feeds the "before" ring
// and completes the "after" context of the pending event of its source.
func (c *Correlator) Observe(obs *model.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()

	state := c.source(obs.Source)

	state.ring.Add(*obs)

	if state.pending != nil && len(state.pending.after) < c.config.ContextAfter {
		state.pending.after = append(state.pending.after, *obs)
	}
}

// Add merges the signals produced for an observation into the pending
// event of its source, creating it if needed.
func (c *Correlator) Add(obs *model.Observation, signals []model.Signal) {
	if len(signals) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	state := c.source(obs.Source)

	if state.pending == nil {
		state.pending = &pendingEvent{
			firstReceivedAt: c.now(),
			firstSignalAt:   signals[0].Timestamp,
			signals:         map[string]*aggregatedSignal{},
			before:          state.ring.Last(c.config.ContextBefore),
		}
	}

	for _, signal := range signals {
		key := signalKey(signal)

		aggregated, exists := state.pending.signals[key]
		if !exists {
			state.pending.signals[key] = &aggregatedSignal{signal: signal, count: 1}
			continue
		}

		aggregated.count++
		if signal.Score > aggregated.signal.Score {
			aggregated.signal = signal
		}
	}
}

// Flush emits the pending events whose window expired; force emits them
// all (final flush before shutdown).
func (c *Correlator) Flush(force bool, emit func(evt model.Event)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()

	for source, state := range c.sources {
		if state.pending == nil {
			continue
		}

		if !force && now.Sub(state.pending.firstReceivedAt) < c.config.Window {
			continue
		}

		emit(c.build(source, state.pending))
		state.pending = nil
	}
}

func (c *Correlator) source(name string) *sourceState {
	state, exists := c.sources[name]
	if !exists {
		state = &sourceState{
			ring: window.NewRing(c.config.ContextBefore),
		}
		c.sources[name] = state
	}

	return state
}

func (c *Correlator) build(source string, pending *pendingEvent) model.Event {
	signals := make([]model.Signal, 0, len(pending.signals))

	var (
		confidenceInverse = 1.0
		modalities        = map[model.Modality]bool{}
		totalCount        int64
	)

	for _, aggregated := range pending.signals {
		signal := aggregated.signal

		if signal.Attributes == nil {
			signal.Attributes = map[string]string{}
		}
		signal.Attributes["occurrences"] = strconv.FormatInt(aggregated.count, 10)

		signals = append(signals, signal)

		confidenceInverse *= 1 - min(signal.Score, 0.99)
		modalities[signal.Modality] = true
		totalCount += aggregated.count
	}

	sort.Slice(signals, func(i, j int) bool {
		if signals[i].Score != signals[j].Score {
			return signals[i].Score > signals[j].Score
		}

		return signals[i].Kind < signals[j].Kind
	})

	dominant := signals[0]

	confidence := min(1-confidenceInverse, 0.99)

	multimodal := modalities[model.ModalityLog] && modalities[model.ModalityMetric]

	kind := "anomaly." + dominant.Kind
	if len(signals) > 1 {
		kind = "anomaly.correlated"
	}

	summary := dominant.Summary
	if len(signals) > 1 {
		summary = fmt.Sprintf("%s (+%d correlated signals)", dominant.Summary, len(signals)-1)
	}

	severity := model.SeverityInfo
	switch {
	case confidence >= 0.85:
		severity = model.SeverityCritical
	case confidence >= 0.6:
		severity = model.SeverityWarning
	}

	return model.Event{
		ID:         model.NewID(),
		Kind:       kind,
		Source:     source,
		Timestamp:  pending.firstSignalAt,
		Severity:   severity,
		Confidence: confidence,
		Summary:    summary,
		Signals:    signals,
		Context: model.Context{
			Before: pending.before,
			After:  pending.after,
		},
		Attributes: map[string]string{
			"signal_count":     strconv.Itoa(len(signals)),
			"signal_instances": strconv.FormatInt(totalCount, 10),
			"multimodal":       strconv.FormatBool(multimodal),
		},
	}
}

func signalKey(signal model.Signal) string {
	key := signal.Kind

	if template, exists := signal.Attributes["template_id"]; exists {
		key += "/" + template
	}

	if metric, exists := signal.Attributes["metric"]; exists {
		key += "/" + metric
	}

	return key
}
