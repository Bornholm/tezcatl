package processor

import (
	"context"

	"github.com/bornholm/tezcatl/internal/core/correlate"
	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
)

// Analysis runs the anomaly detectors on every observation and feeds
// their signals into the correlator, which emits contextualized events
// once their correlation window expires.
type Analysis struct {
	detectors  []detect.Detector
	correlator *correlate.Correlator
}

func NewAnalysis(correlator *correlate.Correlator, detectors ...detect.Detector) *Analysis {
	return &Analysis{
		detectors:  detectors,
		correlator: correlator,
	}
}

func (a *Analysis) Name() string {
	return "analysis"
}

func (a *Analysis) Process(ctx context.Context, obs *model.Observation, emit port.EmitFunc) (bool, error) {
	// Feed the context windows first so the triggering observation is
	// part of the "before" context and only later ones land in "after".
	a.correlator.Observe(obs)

	signals := []model.Signal{}
	for _, detector := range a.detectors {
		signals = append(signals, detector.Detect(obs)...)
	}

	a.correlator.Add(obs, signals)

	a.correlator.Flush(false, emit)

	return true, nil
}

// Flush implements port.Flusher so idle sources still see their pending
// events emitted once the correlation window expires.
func (a *Analysis) Flush(ctx context.Context, force bool, emit port.EmitFunc) {
	a.correlator.Flush(force, emit)
}
