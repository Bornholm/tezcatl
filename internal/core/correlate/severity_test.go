package correlate

import (
	"testing"

	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/model"
)

func signal(kind string, modality model.Modality) model.Signal {
	return model.Signal{Kind: kind, Modality: modality, Score: 0.9}
}

// TestSeverityAsksForCorroboration is the calibration the dogfooding
// instance called for: 43% of its events were "critical", led by a
// load average wobbling on an idle machine, while a service that had
// stopped logging was merely a warning.
func TestSeverityAsksForCorroboration(t *testing.T) {
	lone := []model.Signal{signal("metric.zscore", model.ModalityMetric)}

	for name, test := range map[string]struct {
		confidence float64
		signals    []model.Signal
		multimodal bool
		nearChange bool
		want       model.Severity
	}{
		"a lone z-score, however extreme": {
			confidence: 0.99, signals: lone, want: model.SeverityWarning,
		},
		"the same z-score with a log agreeing": {
			confidence: 0.99, signals: lone, multimodal: true, want: model.SeverityCritical,
		},
		"the same z-score right after a deployment": {
			confidence: 0.99, signals: lone, nearChange: true, want: model.SeverityCritical,
		},
		"a threshold an operator set": {
			confidence: 0.9,
			signals:    []model.Signal{signal(detect.SignalMetricThreshold, model.ModalityMetric)},
			want:       model.SeverityCritical,
		},
		"a template an operator called a symptom": {
			confidence: 0.9,
			signals:    []model.Signal{signal(detect.SignalLogSymptomatic, model.ModalityLog)},
			want:       model.SeverityCritical,
		},
		"a moderate deviation, corroborated or not": {
			confidence: 0.7, signals: lone, multimodal: true, want: model.SeverityWarning,
		},
		"barely a deviation": {
			confidence: 0.4, signals: lone, want: model.SeverityInfo,
		},
	} {
		got := severityOf(test.confidence, test.signals, test.multimodal, test.nearChange)
		if got != test.want {
			t.Errorf("%s: got %q, want %q", name, got, test.want)
		}
	}
}
