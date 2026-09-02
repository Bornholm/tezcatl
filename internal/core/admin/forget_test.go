package admin

import (
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/drain"
	"github.com/bornholm/tezcatl/internal/core/model"
)

func testLogConfig() *detect.LogConfig {
	return &detect.LogConfig{
		LearningPeriod:      time.Minute,
		SpikeBucket:         time.Minute,
		SpikeFactor:         3,
		SpikeMinCount:       5,
		RareThreshold:       2,
		RareMinObservations: 100,
	}
}

func learn(t *testing.T, miner *drain.PartitionedMiner, logs *detect.LogDetector, source string, line string) {
	t.Helper()

	partition, err := miner.Partition(source)
	if err != nil {
		t.Fatal(err)
	}

	result := partition.AddLogMessage(line)

	logs.Detect(&model.Observation{
		ID:        model.NewID(),
		Source:    source,
		Modality:  model.ModalityLog,
		Timestamp: time.Now(),
		Log: &model.LogRecord{
			Raw:        line,
			Template:   result.Cluster.Template(),
			TemplateID: "1",
		},
	})
}

// TestForgetDropsAPartitionAndKeepsTheRest covers the state the
// dogfooding instance was left in: a hundred partitions named after
// login sessions that will never come back, holding two learned
// templates out of three.
func TestForgetDropsAPartitionAndKeepsTheRest(t *testing.T) {
	miner := drain.NewPartitionedMiner(&drain.Config{})
	logs := detect.NewLogDetector(testLogConfig())
	metrics := detect.NewMetricDetector(nil)

	service := NewService(miner, logs, metrics)

	for _, source := range []string{"production/session-101", "production/session-102", "production/blog"} {
		learn(t, miner, logs, source, "connection from somewhere")
		learn(t, miner, logs, source, "another line entirely")

		metrics.Detect(&model.Observation{
			ID:        model.NewID(),
			Source:    source,
			Modality:  model.ModalityMetric,
			Timestamp: time.Now(),
			Metric:    &model.MetricSample{Name: "queue_depth", Value: 12},
		})
	}

	// A marking is a decision, not learning: it must survive.
	if err := service.MarkTemplate("another line entirely", detect.MarkingIgnore); err != nil {
		t.Fatal(err)
	}

	result, err := service.Forget("production/session-*")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Partitions) != 2 {
		t.Fatalf("dropped %v, want the two sessions", result.Partitions)
	}

	if result.Templates != 4 {
		t.Errorf("reported %d templates dropped, want 4", result.Templates)
	}

	if result.Series != 2 {
		t.Errorf("reported %d series dropped, want 2", result.Series)
	}

	for _, partition := range miner.Partitions() {
		if partition != "production/blog" {
			t.Errorf("%s should have been forgotten", partition)
		}
	}

	if len(service.Metrics()) != 1 {
		t.Errorf("the blog's series should be the only one left, got %d", len(service.Metrics()))
	}

	if marking := logs.Markings()["another line entirely"]; marking != detect.MarkingIgnore {
		t.Errorf("the marking must survive forgetting, got %q", marking)
	}
}

func TestForgetRefusesNonsense(t *testing.T) {
	service := NewService(drain.NewPartitionedMiner(&drain.Config{}), detect.NewLogDetector(testLogConfig()), nil)

	if _, err := service.Forget(""); err == nil {
		t.Error("an empty pattern must be refused rather than forgetting everything")
	}

	if _, err := service.Forget("production/["); err == nil {
		t.Error("a malformed glob must be refused")
	}
}

// TestForgetIsScopedToTheGlob guards the blast radius: a pattern must
// not reach across the path separator.
func TestForgetIsScopedToTheGlob(t *testing.T) {
	miner := drain.NewPartitionedMiner(&drain.Config{})
	logs := detect.NewLogDetector(testLogConfig())

	service := NewService(miner, logs, nil)

	for _, source := range []string{"production/blog", "staging/blog"} {
		learn(t, miner, logs, source, "hello")
	}

	result, err := service.Forget("production/*")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Partitions) != 1 || result.Partitions[0] != "production/blog" {
		t.Fatalf("dropped %v, want only production/blog", result.Partitions)
	}
}
