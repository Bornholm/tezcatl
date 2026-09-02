// Package dampen keeps a detector from saying the same thing all day.
//
// A detector is memoryless about its own output: a template that spikes
// every twenty minutes produces a signal every twenty minutes, and an
// operator who reads the first twelve stops reading the thirteenth. The
// dampener is the missing memory. It lets a repeat through only when it
// says something new: the deviation grew, or enough time passed that
// the reader has moved on.
//
// It never silences a pattern for good. That decision is a marking, and
// it belongs to a person.
package dampen

import (
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

const (
	// DefaultCooldown is the first silence after a report. It doubles
	// with each repeat, so a pattern that keeps saying the same thing
	// says it a handful of times a day rather than every hour.
	DefaultCooldown = time.Hour
	// DefaultMaxCooldown caps that doubling: whatever it repeats, a
	// pattern still active is worth a line twice a day.
	DefaultMaxCooldown = 12 * time.Hour
	// DefaultEscalation is how much higher the score must be for a
	// repeat to speak up before its cooldown ends. Scores live in
	// [0,1], so a tenth is a visible worsening rather than jitter.
	DefaultEscalation = 0.1
	// DefaultMaxTracked bounds the memory. Past it the least recently
	// seen patterns are forgotten, which at worst lets one extra
	// signal through.
	DefaultMaxTracked = 20000
)

// Attributes a dampened signal carries when it finally reports.
const (
	// AttrSuppressed counts the repeats held back since the last
	// report, so a reader knows a single line stands for many.
	AttrSuppressed = "dampen.suppressed"
	// AttrEscalated marks a repeat let through because it got worse,
	// carrying the score it beat.
	AttrEscalated = "dampen.escalated_from"
	// AttrStreak counts how many times in a row this pattern has been
	// reported without going quiet, which is how far its silence has
	// been stretched.
	AttrStreak = "dampen.repeats"
)

type Config struct {
	// Cooldown is the first silence after a report. Zero or less
	// disables dampening entirely.
	Cooldown time.Duration
	// MaxCooldown caps the doubling.
	MaxCooldown time.Duration
	// Escalation is the score increase that beats the cooldown.
	Escalation float64
	// MaxTracked bounds how many patterns are remembered.
	MaxTracked int
}

func (c Config) withDefaults() Config {
	if c.MaxCooldown <= 0 {
		c.MaxCooldown = DefaultMaxCooldown
	}

	if c.MaxCooldown < c.Cooldown {
		c.MaxCooldown = c.Cooldown
	}

	if c.Escalation <= 0 {
		c.Escalation = DefaultEscalation
	}

	if c.MaxTracked <= 0 {
		c.MaxTracked = DefaultMaxTracked
	}

	return c
}

// Dampener is safe for concurrent use: the pipeline runs one worker per
// partition, and a pattern can be seen by several.
type Dampener struct {
	config Config

	reported   atomic.Int64
	suppressed atomic.Int64

	mutex    sync.Mutex
	patterns map[string]*pattern
}

// Stats reports what the dampener has done, for the pipeline's health
// line. Holding signals back is only acceptable if the operator can
// see how much is being held.
func (d *Dampener) Stats() []slog.Attr {
	return []slog.Attr{
		slog.Int64("signals_reported", d.reported.Load()),
		slog.Int64("signals_dampened", d.suppressed.Load()),
		slog.Int("patterns_tracked", d.Tracked()),
	}
}

type pattern struct {
	// reportedAt is when this pattern last reached a reader.
	reportedAt time.Time
	// lastSeen is when it last occurred, reported or not, and drives
	// eviction.
	lastSeen time.Time
	// maxScore is the strongest score reported so far: the bar a
	// repeat must clear to interrupt the cooldown.
	maxScore float64
	// suppressed counts the repeats held back since the last report.
	suppressed int64
	// streak counts the reports made without the pattern going quiet.
	// It is what stretches the silence: a pattern nobody acted on
	// after five reports will not be improved by a sixth on the hour.
	streak int
}

// silence is how long this pattern must stay quiet before speaking
// again: the base cooldown doubled once per report in the streak.
func (p *pattern) silence(config Config) time.Duration {
	required := config.Cooldown

	for range p.streak {
		required *= 2

		if required >= config.MaxCooldown {
			return config.MaxCooldown
		}
	}

	return required
}

func New(config Config) *Dampener {
	return &Dampener{
		config:   config.withDefaults(),
		patterns: map[string]*pattern{},
	}
}

// Filter returns the signals worth reporting, annotating those that
// stand for suppressed repeats. Signals are judged on the clock of the
// observation that produced them, so a replay of yesterday's logs
// dampens the way yesterday would have.
func (d *Dampener) Filter(signals []model.Signal) []model.Signal {
	if d.config.Cooldown <= 0 || len(signals) == 0 {
		return signals
	}

	d.mutex.Lock()
	defer d.mutex.Unlock()

	kept := make([]model.Signal, 0, len(signals))

	for _, signal := range signals {
		if reported, attributes := d.admit(signal); !reported {
			d.suppressed.Add(1)
		} else {
			d.reported.Add(1)

			for key, value := range attributes {
				if signal.Attributes == nil {
					signal.Attributes = map[string]string{}
				}

				signal.Attributes[key] = value
			}

			kept = append(kept, signal)
		}
	}

	return kept
}

// admit decides one signal's fate and updates the pattern's memory.
func (d *Dampener) admit(signal model.Signal) (bool, map[string]string) {
	key := Key(signal)

	entry, known := d.patterns[key]
	if !known {
		d.makeRoom()

		d.patterns[key] = &pattern{
			reportedAt: signal.Timestamp,
			lastSeen:   signal.Timestamp,
			maxScore:   signal.Score,
		}

		return true, nil
	}

	// A pattern that stopped occurring for longer than its own
	// silence has gone away; when it comes back it is news again,
	// and its next repeat gets the base cooldown rather than the
	// stretched one.
	returning := signal.Timestamp.Sub(entry.lastSeen) > entry.silence(d.config)
	if returning {
		entry.streak = 0
	}

	entry.lastSeen = signal.Timestamp

	quiet := signal.Timestamp.Sub(entry.reportedAt)

	report := func(attributes map[string]string) (bool, map[string]string) {
		if entry.suppressed > 0 {
			if attributes == nil {
				attributes = map[string]string{}
			}

			attributes[AttrSuppressed] = strconv.FormatInt(entry.suppressed, 10)
		}

		if entry.streak > 0 {
			if attributes == nil {
				attributes = map[string]string{}
			}

			attributes[AttrStreak] = strconv.Itoa(entry.streak)
		}

		entry.reportedAt = signal.Timestamp
		entry.suppressed = 0

		return true, attributes
	}

	switch {
	case signal.Score >= entry.maxScore+d.config.Escalation:
		// It got worse: that is new information, and the pattern
		// earns a fresh hearing.
		attributes := map[string]string{
			AttrEscalated: strconv.FormatFloat(entry.maxScore, 'f', 2, 64),
		}

		entry.maxScore = signal.Score
		entry.streak = 0

		return report(attributes)

	case quiet >= entry.silence(d.config):
		if !returning {
			entry.streak++
		}

		// The bar decays with the silence: a pattern that reported a
		// 0.9 this morning should not need a 1.0 to be heard tonight.
		entry.maxScore = signal.Score

		return report(nil)

	default:
		entry.suppressed++

		return false, nil
	}
}

// Key names the recurring thing behind a signal: the same template on
// the same source, or the same series. Two signals sharing a key say
// the same sentence.
func Key(signal model.Signal) string {
	identity := signal.Attributes["template"]
	if identity == "" {
		identity = signal.Attributes["series"]
	}

	if identity == "" {
		identity = signal.Attributes["metric"]
	}

	if identity == "" {
		// Nothing names the pattern; fall back to the summary, which
		// at least groups literal repeats.
		identity = signal.Summary
	}

	return signal.Kind + "\x00" + signal.Source + "\x00" + identity
}

// makeRoom evicts the least recently seen patterns. Called with the
// lock held.
func (d *Dampener) makeRoom() {
	if len(d.patterns) < d.config.MaxTracked {
		return
	}

	type aged struct {
		key      string
		lastSeen time.Time
	}

	entries := make([]aged, 0, len(d.patterns))
	for key, entry := range d.patterns {
		entries = append(entries, aged{key: key, lastSeen: entry.lastSeen})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].lastSeen.Before(entries[j].lastSeen)
	})

	// Drop a tenth at a time rather than one per insert, so a full
	// dampener does not sort on every signal.
	for i := 0; i < len(entries)/10+1; i++ {
		delete(d.patterns, entries[i].key)
	}
}

// Tracked reports how many patterns are remembered, for the stats line.
func (d *Dampener) Tracked() int {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	return len(d.patterns)
}
