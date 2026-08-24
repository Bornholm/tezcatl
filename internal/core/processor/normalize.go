package processor

import (
	"context"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/pkg/errors"
)

const DefaultMaxLogLength = 8192

// Normalize validates observations and fills the missing fields the rest
// of the pipeline relies on. Invalid observations are dropped with an
// error so the engine can account for them.
type Normalize struct {
	maxLogLength int
	now          func() time.Time
}

type NormalizeOptionFunc func(n *Normalize)

func WithMaxLogLength(length int) NormalizeOptionFunc {
	return func(n *Normalize) {
		n.maxLogLength = length
	}
}

func NewNormalize(funcs ...NormalizeOptionFunc) *Normalize {
	n := &Normalize{
		maxLogLength: DefaultMaxLogLength,
		now:          time.Now,
	}

	for _, fn := range funcs {
		fn(n)
	}

	return n
}

func (n *Normalize) Name() string {
	return "normalize"
}

func (n *Normalize) Process(ctx context.Context, obs *model.Observation, emit port.EmitFunc) (bool, error) {
	if obs.Service != "" {
		if obs.Environment == "" {
			obs.Environment = model.DefaultEnvironment
		}

		obs.Source = obs.Environment + "/" + obs.Service
	}

	if obs.Source == "" {
		return false, errors.New("missing service or source")
	}

	switch obs.Modality {
	case model.ModalityLog:
		if obs.Log == nil {
			return false, errors.New("log observation without log record")
		}

		if len(obs.Log.Raw) > n.maxLogLength {
			obs.Log.Raw = obs.Log.Raw[:n.maxLogLength]
		}

		if len(obs.Log.Message) > n.maxLogLength {
			obs.Log.Message = obs.Log.Message[:n.maxLogLength]
		}

	case model.ModalityMetric:
		if obs.Metric == nil {
			return false, errors.New("metric observation without metric sample")
		}

		if obs.Metric.Name == "" {
			return false, errors.New("metric observation without metric name")
		}

	case model.ModalityChange:
		if obs.Change == nil {
			return false, errors.New("change observation without change record")
		}

		if obs.Change.Type == "" {
			return false, errors.New("change observation without change type")
		}

	default:
		return false, errors.Errorf("unsupported modality %q", obs.Modality)
	}

	now := n.now()

	if obs.IngestedAt.IsZero() {
		obs.IngestedAt = now
	}

	if obs.Timestamp.IsZero() {
		obs.Timestamp = obs.IngestedAt
	}

	if obs.ID == "" {
		obs.ID = model.NewID()
	}

	return true, nil
}
