package processor

import (
	"context"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
)

// Debug turns every observation into a "debug.observation" event. It is
// meant to validate the pipeline end to end before real detectors exist,
// and as a tracing aid afterwards.
type Debug struct{}

func NewDebug() *Debug {
	return &Debug{}
}

func (d *Debug) Name() string {
	return "debug"
}

func (d *Debug) Process(ctx context.Context, obs *model.Observation, emit port.EmitFunc) (bool, error) {
	evt := model.Event{
		ID:         model.NewID(),
		Kind:       "debug.observation",
		Source:     obs.Source,
		Timestamp:  obs.Timestamp,
		Severity:   model.SeverityInfo,
		Confidence: 1,
		Attributes: map[string]string{
			"observation_id": obs.ID,
			"modality":       string(obs.Modality),
		},
	}

	switch {
	case obs.Log != nil:
		evt.Summary = obs.Log.EffectiveMessage()
	case obs.Metric != nil:
		evt.Summary = obs.Metric.Name
	case obs.Change != nil:
		evt.Summary = obs.Change.Type + " " + obs.Change.Version
	}

	emit(evt)

	return true, nil
}
