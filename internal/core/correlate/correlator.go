package correlate

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
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
	// EchoWindow is how long after a change the signals of every other
	// source are folded into one event attached to that change, rather
	// than one event per source. Zero disables the fold.
	EchoWindow time.Duration `yaml:"echo_window"`
}

// DefaultEchoWindow covers a deployment from the moment it is declared
// to the moment the host settles. Measured on the dogfooding instance:
// the longest deploy, build included, took seven minutes from the first
// build line to the last restarted unit.
const DefaultEchoWindow = 10 * time.Minute

func DefaultConfig() *Config {
	return &Config{
		Window:        30 * time.Second,
		ContextBefore: 10,
		ContextAfter:  10,
		Clock:         ClockWall,
		ChangeHorizon: 15 * time.Minute,
		EchoWindow:    DefaultEchoWindow,
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
	// echoes holds, per source that declared a change, the signals the
	// rest of the host produced in its wake.
	echoes map[string]*echoState
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

// echoState is the event a change leaves behind on the other sources.
//
// A deployment does not stay inside the service it deploys: the kernel
// logs the veth coming and going, udev the interface, networkd the lost
// carrier, systemd the scopes that succeeded, docker the restart, and
// the host's load climbs. Each of those is a frequency spike on a
// source with no change of its own, so each became its own event. On
// the dogfooding instance, 42 of 99 events in three days were that
// wake, up to 20 for a single deploy. They are one fact, and the fact
// is the deploy.
type echoState struct {
	changes        []model.Observation
	service        string
	environment    string
	lastChangeAt   time.Time
	lastReceivedAt time.Time
	firstSignalAt  time.Time
	signals        map[string]*aggregatedSignal
	sources        map[string]int64
}

func NewCorrelator(config *Config) *Correlator {
	if config == nil {
		config = DefaultConfig()
	}

	return &Correlator{
		config:  config,
		now:     time.Now,
		sources: map[string]*sourceState{},
		echoes:  map[string]*echoState{},
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
		c.openEcho(obs)
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

// openEcho starts, or extends, the echo of a change. A second change
// on the same source while its echo is open (Dokku declares the build
// and then the deploy) keeps the same echo and pushes its end back.
func (c *Correlator) openEcho(change *model.Observation) {
	if c.config.EchoWindow <= 0 || change.Change == nil {
		return
	}

	echo, exists := c.echoes[change.Source]
	if !exists {
		echo = &echoState{
			service:     change.Service,
			environment: change.Environment,
			signals:     map[string]*aggregatedSignal{},
			sources:     map[string]int64{},
		}
		c.echoes[change.Source] = echo
	}

	echo.changes = append(echo.changes, *change)
	if excess := len(echo.changes) - maxTrackedChanges; excess > 0 {
		echo.changes = echo.changes[excess:]
	}

	echo.lastChangeAt = change.Timestamp
	echo.lastReceivedAt = c.now()
}

// echoFor finds the change whose wake a signal belongs to: the latest
// one declared within the echo window, on a source other than the
// signal's own. A source that declared its own change is the subject
// of that change, not its echo, and keeps its own event with the
// change attached.
func (c *Correlator) echoFor(state *sourceState, source string, at time.Time) *echoState {
	if c.config.EchoWindow <= 0 || len(c.echoes) == 0 {
		return nil
	}

	state.pruneChanges(c.watermark, c.config.ChangeHorizon)
	if len(state.changes) > 0 {
		return nil
	}

	var found *echoState

	for changed, echo := range c.echoes {
		if changed == source {
			continue
		}

		offset := at.Sub(echo.lastChangeAt)
		if offset < 0 || offset > c.config.EchoWindow {
			continue
		}

		if found == nil || echo.lastChangeAt.After(found.lastChangeAt) {
			found = echo
		}
	}

	return found
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

	if echo := c.echoFor(state, obs.Source, signals[0].Timestamp); echo != nil {
		// What a person asked to hear about is never folded: a
		// threshold crossed or a symptom seen during someone else's
		// deploy is still that threshold, that symptom.
		own := make([]model.Signal, 0, len(signals))

		for _, signal := range signals {
			if intended(signal) {
				own = append(own, signal)
				continue
			}

			if echo.firstSignalAt.IsZero() || signal.Timestamp.Before(echo.firstSignalAt) {
				echo.firstSignalAt = signal.Timestamp
			}

			// Two sources spiking on their own template "1" are two
			// facts, so the echo keys by source as well.
			echo.sources[obs.Source]++
			merge(echo.signals, obs.Source+"\x00"+signalKey(signal), signal)
		}

		signals = own
		if len(signals) == 0 {
			return
		}
	}

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
		merge(state.pending.signals, signalKey(signal), signal)
	}
}

// merge folds a signal into an aggregate, keeping the strongest
// instance of each key and counting the rest.
func merge(into map[string]*aggregatedSignal, key string, signal model.Signal) {
	aggregated, exists := into[key]
	if !exists {
		into[key] = &aggregatedSignal{signal: signal, count: 1}
		return
	}

	aggregated.count++
	if signal.Score > aggregated.signal.Score {
		aggregated.signal = signal
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

	for source, echo := range c.echoes {
		if !force && !c.echoExpired(echo, now) {
			continue
		}

		if len(echo.signals) > 0 {
			emit(c.buildEcho(source, echo))
		}

		delete(c.echoes, source)
	}
}

func (c *Correlator) echoExpired(echo *echoState, now time.Time) bool {
	if c.config.Clock == ClockEvent {
		return c.watermark.Sub(echo.lastChangeAt) >= c.config.EchoWindow
	}

	return now.Sub(echo.lastReceivedAt) >= c.config.EchoWindow
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

// combined is what a set of aggregated signals says together: the
// signals strongest first, the confidence they add up to, whether two
// modalities agree, and how many instances they stand for.
type combined struct {
	signals    []model.Signal
	confidence float64
	multimodal bool
	instances  int64
}

func combine(aggregated map[string]*aggregatedSignal) combined {
	signals := make([]model.Signal, 0, len(aggregated))

	var (
		confidenceInverse = 1.0
		modalities        = map[model.Modality]bool{}
		totalCount        int64
	)

	for _, aggregated := range aggregated {
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

	return combined{
		signals:    signals,
		confidence: min(1-confidenceInverse, 0.99),
		multimodal: modalities[model.ModalityLog] && modalities[model.ModalityMetric],
		instances:  totalCount,
	}
}

// buildEcho emits the wake of a change as one event on the source that
// declared it. It stops at warning by construction: the change is what
// explains these signals, so it cannot also be what corroborates them.
// Anything a person asked to hear about never reached the echo.
func (c *Correlator) buildEcho(source string, echo *echoState) model.Event {
	all := combine(echo.signals)

	sources := make([]string, 0, len(echo.sources))
	for name := range echo.sources {
		sources = append(sources, name)
	}
	sort.Strings(sources)

	latest := echo.changes[len(echo.changes)-1]

	related := make([]model.RelatedChange, 0, len(echo.changes))
	for _, change := range echo.changes {
		related = append(related, model.RelatedChange{
			Source:        change.Source,
			Change:        *change.Change,
			Timestamp:     change.Timestamp,
			OffsetSeconds: change.Timestamp.Sub(echo.firstSignalAt).Seconds(),
		})
	}

	subject := echo.service
	if subject == "" {
		subject = source
	}

	return model.Event{
		ID:          model.NewID(),
		Kind:        "anomaly.change_echo",
		Source:      source,
		Service:     echo.service,
		Environment: echo.environment,
		Timestamp:   echo.firstSignalAt,
		Severity:    severityOf(all.confidence, all.signals, false, false),
		Confidence:  all.confidence,
		Summary: fmt.Sprintf("%d signals from %d sources in the wake of %s %s: %s",
			len(all.signals), len(sources), latest.Change.Type, subject, strings.Join(sources, ", ")),
		Signals:        all.signals,
		RelatedChanges: related,
		Attributes: map[string]string{
			"signal_count":     strconv.Itoa(len(all.signals)),
			"signal_instances": strconv.FormatInt(all.instances, 10),
			"multimodal":       strconv.FormatBool(all.multimodal),
			"sources":          strings.Join(sources, ","),
		},
	}
}

func (c *Correlator) build(source string, state *sourceState) model.Event {
	pending := state.pending

	all := combine(pending.signals)
	signals, confidence, multimodal, totalCount := all.signals, all.confidence, all.multimodal, all.instances

	dominant := signals[0]

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
// declared right before a symptom in the logs, or an operator's own
// judgement already recorded: a symptomatic template, a threshold
// they set, a heartbeat they asked to be told about.
// Without it the strongest deviation stops at warning, which is
// exactly what it deserves: worth reading, not worth waking up for.
//
// A change corroborates a log, not a metric. A new error template
// seconds after a deploy is the deploy talking, and that is worth a
// wake-up. A container's CPU climbing seconds after its own restart,
// or two containers running while the old one hands over, is the
// restart itself: the change explains the number rather than
// aggravating it. On the dogfooding instance, three of four critical
// events near a deploy were of that second kind.
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

	asked, logged := false, false
	for _, signal := range signals {
		asked = asked || intended(signal)
		logged = logged || signal.Modality == model.ModalityLog
	}

	if multimodal || (nearChange && logged) || asked {
		return model.SeverityCritical
	}

	return model.SeverityWarning
}

// intended tells a signal that carries a human decision from one that
// carries a measurement: a template someone called a symptom, a bound
// someone set, a heartbeat someone asked to be told about when it
// stops.
func intended(signal model.Signal) bool {
	switch signal.Kind {
	case detect.SignalLogSymptomatic, detect.SignalMetricThreshold:
		return true
	case detect.SignalLogMissingTemplate:
		return signal.Attributes["marking"] == string(detect.MarkingHeartbeat)
	}

	return false
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
