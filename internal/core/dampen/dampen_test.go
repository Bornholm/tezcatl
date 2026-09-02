package dampen

import (
	"strconv"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func spike(at time.Time, score float64) model.Signal {
	return model.Signal{
		Kind:      "log.frequency_spike",
		Modality:  model.ModalityLog,
		Source:    "production/blog",
		Timestamp: at,
		Score:     score,
		Summary:   "frequency spike for template",
		Attributes: map[string]string{
			"template": `<IP> - - <*> "GET <*> HTTP/<NUM>"`,
		},
	}
}

// TestRepeatsAreHeldBack is the dogfooding number that started this:
// one access-log template spiked 22 times in a day, all saying the
// same thing.
func TestRepeatsAreHeldBack(t *testing.T) {
	dampener := New(Config{Cooldown: time.Hour})

	start := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

	reported := 0
	for i := range 22 {
		// One spike every twenty minutes, all equally strong.
		at := start.Add(time.Duration(i) * 20 * time.Minute)
		reported += len(dampener.Filter([]model.Signal{spike(at, 0.66)}))
	}

	// Seven hours and twenty minutes of spikes: the first, then one
	// after an hour, two, four... a handful, not one per hour.
	if reported > 5 {
		t.Fatalf("22 identical spikes reported %d times, want a handful", reported)
	}

	if reported < 2 {
		t.Fatalf("dampening must not silence a pattern for good, got %d reports", reported)
	}
}

func TestFirstOccurrenceAlwaysReports(t *testing.T) {
	dampener := New(Config{Cooldown: time.Hour})

	at := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

	if got := dampener.Filter([]model.Signal{spike(at, 0.5)}); len(got) != 1 {
		t.Fatal("a pattern never seen before must always be reported")
	}

	if got := dampener.Filter([]model.Signal{spike(at.Add(time.Minute), 0.5)}); len(got) != 0 {
		t.Fatal("the same signal a minute later must be held back")
	}
}

// TestWorseningBeatsTheCooldown is the whole reason dampening is not
// just a rate limit: an operator must hear when it gets worse.
func TestWorseningBeatsTheCooldown(t *testing.T) {
	dampener := New(Config{Cooldown: time.Hour, Escalation: 0.1})

	at := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

	dampener.Filter([]model.Signal{spike(at, 0.5)})

	if got := dampener.Filter([]model.Signal{spike(at.Add(time.Minute), 0.55)}); len(got) != 0 {
		t.Fatal("a jitter of 0.05 must not beat the cooldown")
	}

	got := dampener.Filter([]model.Signal{spike(at.Add(2*time.Minute), 0.7)})
	if len(got) != 1 {
		t.Fatal("a clearly worse occurrence must be reported at once")
	}

	if from := got[0].Attributes[AttrEscalated]; from != "0.50" {
		t.Errorf("the report must say which score it beat, got %q", from)
	}

	if suppressed := got[0].Attributes[AttrSuppressed]; suppressed != "1" {
		t.Errorf("it must also account for the one held back, got %q", suppressed)
	}
}

func TestReportAfterCooldownCountsWhatItStandsFor(t *testing.T) {
	dampener := New(Config{Cooldown: time.Hour})

	at := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

	dampener.Filter([]model.Signal{spike(at, 0.6)})

	for i := 1; i <= 5; i++ {
		dampener.Filter([]model.Signal{spike(at.Add(time.Duration(i)*10*time.Minute), 0.6)})
	}

	got := dampener.Filter([]model.Signal{spike(at.Add(70*time.Minute), 0.6)})
	if len(got) != 1 {
		t.Fatal("the cooldown must expire")
	}

	if suppressed := got[0].Attributes[AttrSuppressed]; suppressed != "5" {
		t.Errorf("the report stands for 5 held back, got %q", suppressed)
	}
}

// TestPatternsAreToldApart guards the key: two templates, two series,
// or two sources are different sentences.
func TestPatternsAreToldApart(t *testing.T) {
	dampener := New(Config{Cooldown: time.Hour})

	at := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

	other := spike(at, 0.6)
	other.Attributes = map[string]string{"template": "another template"}

	elsewhere := spike(at, 0.6)
	elsewhere.Source = "production/ssh"

	metric := model.Signal{
		Kind:       "metric.zscore",
		Modality:   model.ModalityMetric,
		Source:     "production/host",
		Timestamp:  at,
		Score:      0.9,
		Summary:    "system.load1 deviates",
		Attributes: map[string]string{"series": "production/host/system.load1"},
	}

	got := dampener.Filter([]model.Signal{spike(at, 0.6), other, elsewhere, metric})
	if len(got) != 4 {
		t.Fatalf("four distinct patterns must all report, got %d", len(got))
	}

	// And the same four, a minute later, must all be held back.
	got = dampener.Filter([]model.Signal{
		spike(at.Add(time.Minute), 0.6), other, elsewhere, metric,
	})
	if len(got) != 0 {
		t.Fatalf("the same four must be held back, got %d", len(got))
	}
}

func TestDisabledByZeroCooldown(t *testing.T) {
	dampener := New(Config{})

	at := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

	for range 5 {
		if got := dampener.Filter([]model.Signal{spike(at, 0.6)}); len(got) != 1 {
			t.Fatal("a zero cooldown must let everything through")
		}
	}
}

func TestEvictionKeepsTheMapBounded(t *testing.T) {
	dampener := New(Config{Cooldown: time.Hour, MaxTracked: 100})

	at := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

	for i := range 500 {
		signal := spike(at.Add(time.Duration(i)*time.Second), 0.6)
		signal.Attributes = map[string]string{"template": string(rune('a'+i%26)) + strconv.Itoa(i)}
		dampener.Filter([]model.Signal{signal})
	}

	if tracked := dampener.Tracked(); tracked > 100 {
		t.Fatalf("tracked %d patterns, want at most 100", tracked)
	}
}

// TestSilenceStretchesWithEachRepeat is the lesson from the instance:
// a steady pattern with a flat score walks straight through a plain
// cooldown. The silence has to grow.
func TestSilenceStretchesWithEachRepeat(t *testing.T) {
	dampener := New(Config{Cooldown: time.Hour, MaxCooldown: 12 * time.Hour})

	start := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	reported := []time.Time{}

	// A full day of the same spike, every forty-five minutes.
	for i := range 32 {
		at := start.Add(time.Duration(i) * 45 * time.Minute)
		if len(dampener.Filter([]model.Signal{spike(at, 0.7)})) > 0 {
			reported = append(reported, at)
		}
	}

	if len(reported) > 6 {
		t.Fatalf("a day of the same spike was reported %d times, want six or fewer", len(reported))
	}

	// And the gaps must widen rather than stay flat.
	if len(reported) >= 3 {
		first := reported[1].Sub(reported[0])
		last := reported[len(reported)-1].Sub(reported[len(reported)-2])

		if last <= first {
			t.Errorf("the silence must stretch: first gap %s, last gap %s", first, last)
		}
	}
}

// TestGoingQuietResetsTheBackoff keeps a pattern from being buried
// forever: something that stops and comes back is news again.
func TestGoingQuietResetsTheBackoff(t *testing.T) {
	dampener := New(Config{Cooldown: time.Hour})

	start := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	// A morning burst stretches the silence.
	for i := range 12 {
		dampener.Filter([]model.Signal{spike(start.Add(time.Duration(i)*20*time.Minute), 0.7)})
	}

	// Then a full day of quiet.
	back := start.Add(30 * time.Hour)

	if got := dampener.Filter([]model.Signal{spike(back, 0.7)}); len(got) != 1 {
		t.Fatal("a pattern that went away and came back must be reported")
	}

	// And it starts over from the base cooldown, not from the
	// stretched one.
	if got := dampener.Filter([]model.Signal{spike(back.Add(65*time.Minute), 0.7)}); len(got) != 1 {
		t.Error("after a quiet spell the backoff must start over")
	}
}
