package admin

import (
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/drain"
	"github.com/bornholm/tezcatl/internal/core/model"
)

func TestServiceMarkAndListTemplates(t *testing.T) {
	miner := drain.NewPartitionedMiner(drain.DefaultConfig())

	partition, err := miner.Partition("prod/api")
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	partition.AddLogMessage("user alice logged in")
	partition.AddLogMessage("user bob logged in")
	partition.AddLogMessage("connection reset by peer")

	logConfig := detect.DefaultLogConfig()
	logConfig.LearningPeriod = 0

	detector := detect.NewLogDetector(logConfig)

	service := NewService(miner, detector, nil)

	if err := service.MarkTemplate("connection reset by peer", detect.MarkingIgnore); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if err := service.MarkTemplate("whatever", "bogus"); err == nil {
		t.Fatal("expected invalid marking to be rejected")
	}

	if err := service.MarkTemplate("", detect.MarkingIgnore); err == nil {
		t.Fatal("expected empty template to be rejected")
	}

	templates := service.Templates()
	if len(templates) != 2 {
		t.Fatalf("expected 2 templates, got %+v", templates)
	}

	var marked *TemplateInfo
	for i := range templates {
		if templates[i].Template == "connection reset by peer" {
			marked = &templates[i]
		}
	}

	if marked == nil || marked.Marking != detect.MarkingIgnore {
		t.Fatalf("expected ignore marking to be listed, got %+v", templates)
	}

	// The marking must take effect immediately on detection.
	signals := detector.Detect(&model.Observation{
		ID:        model.NewID(),
		Source:    "prod/api",
		Modality:  model.ModalityLog,
		Timestamp: time.Now(),
		Attributes: map[string]string{
			model.AttrTemplateChangeType: "cluster_created",
		},
		Log: &model.LogRecord{
			Raw:        "connection reset by peer",
			TemplateID: "3",
			Template:   "connection reset by peer",
		},
	})

	if len(signals) != 0 {
		t.Fatalf("expected ignored template to produce no signal, got %+v", signals)
	}

	// Clearing restores the default behavior.
	if err := service.MarkTemplate("connection reset by peer", ""); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if marking := detector.Markings()["connection reset by peer"]; marking != "" {
		t.Fatalf("expected marking to be cleared, got %q", marking)
	}
}

func TestServiceMetrics(t *testing.T) {
	metricDetector := detect.NewMetricDetector(nil)

	for i := range 40 {
		metricDetector.Detect(&model.Observation{
			ID:        model.NewID(),
			Source:    "prod/api",
			Modality:  model.ModalityMetric,
			Timestamp: time.Now(),
			Metric:    &model.MetricSample{Name: "latency_ms", Value: 100 + float64(i%5)},
		})
	}

	service := NewService(nil, nil, metricDetector)

	series := service.Metrics()
	if len(series) != 1 {
		t.Fatalf("expected 1 series, got %+v", series)
	}

	info := series[0]

	if info.Key != "prod/api/latency_ms" || info.Samples != 40 {
		t.Fatalf("unexpected series: %+v", info)
	}

	if info.Mean < 99 || info.Mean > 105 {
		t.Errorf("unexpected mean: %f", info.Mean)
	}

	if info.Warmup {
		t.Error("expected series to be past warmup")
	}

	// Without a metric detector (detection disabled), Metrics is empty.
	if series := NewService(nil, nil, nil).Metrics(); series != nil {
		t.Fatalf("expected no series, got %+v", series)
	}
}
