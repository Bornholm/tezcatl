package detect

import "github.com/bornholm/tezcatl/internal/core/model"

// Detector examines an observation and produces zero or more elementary
// anomaly signals. Implementations keep per-source learned state and must
// be safe for concurrent use across sources.
type Detector interface {
	Name() string
	Detect(obs *model.Observation) []model.Signal
}
