package correlate

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/window"
)

type Clock string

const (
	// ClockWall expires correlation windows on the wall clock: the
	// normal mode for live streams.
	ClockWall Clock = "wall"
	// ClockEvent expires correlation windows on the observation
	// timestamps (watermark): the mode for replaying past incidents
	// with an exact timeline.
	ClockEvent Clock = "event"
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
	// Clock selects how window expiry is measured (wall or event).
	Clock Clock `yaml:"clock"`
	// ChangeHorizon is how far back changes are still attached to an
	// event as related changes.
	ChangeHorizon time.Duration `yaml:"change_horizon"`
}

func DefaultConfig() *Config {
	return &Config{
		Window:        30 * time.Second,
		ContextBefore: 10,
		ContextAfter:  10,
		Clock:         ClockWall,
		ChangeHorizon: 15 * time.Minute,
	}
}

// Correlator aggregates the signals of a same source arriving within a
// time window into a single contextualized event: deduplicated signals, a
// combined confidence score and the observations surrounding the first
// signal. It is safe for concurrent use.
type Correlator struct {
	config *Config
	now    func() time.Time

	mu        sync.Mutex
	watermark time.Time
	sources   map[string]*sourceState
}

type sourceState struct {
	ring    *window.Ring
	changes []model.Observation
	pending *pendingEvent
}

type pendingEvent struct {
	firstReceivedAt time.Time
	firstSignalAt   time.Time
	service         string
	environment     string
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

	if obs.Timestamp.After(c.watermark) {
		c.watermark = obs.Timestamp
	}

	state := c.source(obs.Source)

	state.ring.Add(*obs)

	if obs.Modality == model.ModalityChange {
		state.changes = append(state.changes, *obs)
		state.pruneChanges(c.watermark, c.config.ChangeHorizon)
	}

	if state.pending != nil && len(state.pending.after) < c.config.ContextAfter {
		state.pending.after = append(state.pending.after, *obs)
	}
}

const maxTrackedChanges = 64

func (s *sourceState) pruneChanges(watermark time.Time, horizon time.Duration) {
	for len(s.changes) > 0 && watermark.Sub(s.changes[0].Timestamp) > horizon {
		s.changes = s.changes[1:]
	}

	if excess := len(s.changes) - maxTrackedChanges; excess > 0 {
		s.changes = s.changes[excess:]
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
			service:         obs.Service,
			environment:     obs.Environment,
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

		if !force && !c.expired(state.pending, now) {
			continue
		}

		emit(c.build(source, state))
		state.pending = nil
	}
}

func (c *Correlator) expired(pending *pendingEvent, now time.Time) bool {
	if c.config.Clock == ClockEvent {
		return c.watermark.Sub(pending.firstSignalAt) >= c.config.Window
	}

	return now.Sub(pending.firstReceivedAt) >= c.config.Window
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

func (c *Correlator) build(source string, state *sourceState) model.Event {
	pending := state.pending

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

	changes := c.relatedChanges(state, pending)

	severity := severityOf(confidence, signals, multimodal, len(changes) > 0)

	return model.Event{
		ID:             model.NewID(),
		Kind:           kind,
		Source:         source,
		Service:        pending.service,
		Environment:    pending.environment,
		Timestamp:      pending.firstSignalAt,
		Severity:       severity,
		Confidence:     confidence,
		Summary:        summary,
		Signals:        signals,
		RelatedChanges: changes,
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

// severityOf grades an event. Confidence measures how far an
// observation is from its baseline, which is a statement about
// statistics, not about consequences: a load average wobbling on an
// idle machine reaches 0.99 as easily as a payment gateway failing.
// Calling that "critical" makes the word mean "unusual", and an
// operator who wires an alert on it is woken by arithmetic.
//
// So critical also asks for corroboration, something a lone number
// cannot fake: two modalities agreeing on the same service, a change
// declared right before, or an operator's own judgement already
// recorded: a symptomatic template, a threshold they set, a heartbeat
// they asked to be told about.
// Without it the strongest deviation stops at warning, which is
// exactly what it deserves: worth reading, not worth waking up for.
//
// A site that only collects metrics therefore has one way to reach
// critical: say which values matter, with a threshold. That is the
// intended answer, not a gap. Statistics alone never scream.
func severityOf(confidence float64, signals []model.Signal, multimodal bool, nearChange bool) model.Severity {
	if confidence < 0.6 {
		return model.SeverityInfo
	}

	if confidence < 0.85 {
		return model.SeverityWarning
	}

	intended := false
	for _, signal := range signals {
		// Each of these carries a human decision rather than a
		// measurement: a template someone called a symptom, a bound
		// someone set, and a heartbeat someone asked to be told about
		// when it stops.
		switch {
		case signal.Kind == detect.SignalLogSymptomatic,
			signal.Kind == detect.SignalMetricThreshold,
			signal.Kind == detect.SignalLogMissingTemplate && signal.Attributes["marking"] == string(detect.MarkingHeartbeat):
			intended = true
		}

		if intended {
			break
		}
	}

	if multimodal || nearChange || intended {
		return model.SeverityCritical
	}

	return model.SeverityWarning
}

// relatedChanges surfaces the changes observed shortly before the event
// (within the change horizon) or during its correlation window. Temporal
// proximity is a correlation, not a proof of cause.
func (c *Correlator) relatedChanges(state *sourceState, pending *pendingEvent) []model.RelatedChange {
	if len(state.changes) == 0 {
		return nil
	}

	var related []model.RelatedChange

	for _, change := range state.changes {
		offset := change.Timestamp.Sub(pending.firstSignalAt)

		if offset < -c.config.ChangeHorizon || offset > c.config.Window {
			continue
		}

		related = append(related, model.RelatedChange{
			Source:        change.Source,
			Change:        *change.Change,
			Timestamp:     change.Timestamp,
			OffsetSeconds: offset.Seconds(),
		})
	}

	return related
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
