package admin

import (
	"sort"

	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/drain"
	"github.com/pkg/errors"
)

// Service exposes the runtime inspection and feedback operations:
// learned templates and their markings, learned metric series. It is
// transport-agnostic; the gRPC admin server and the offline CLI both
// compose it.
type Service struct {
	miner          *drain.PartitionedMiner
	logDetector    *detect.LogDetector
	metricDetector *detect.MetricDetector
}

type TemplateInfo struct {
	Partition string         `json:"partition"`
	ID        int64          `json:"id"`
	Template  string         `json:"template"`
	Size      int64          `json:"size"`
	Marking   detect.Marking `json:"marking,omitempty"`
}

func NewService(miner *drain.PartitionedMiner, logDetector *detect.LogDetector, metricDetector *detect.MetricDetector) *Service {
	return &Service{
		miner:          miner,
		logDetector:    logDetector,
		metricDetector: metricDetector,
	}
}

// Metrics lists the learned metric series with their baselines.
func (s *Service) Metrics() []detect.SeriesInfo {
	if s.metricDetector == nil {
		return nil
	}

	return s.metricDetector.Series()
}

// MarkTemplate overrides the behavior of a template at runtime. An empty
// marking clears the override.
func (s *Service) MarkTemplate(template string, marking detect.Marking) error {
	if s.logDetector == nil {
		return errors.New("log detection is disabled")
	}

	if template == "" {
		return errors.New("missing template")
	}

	if err := s.logDetector.SetMarking(template, marking); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// Templates lists the learned templates across every partition, with
// their current marking.
func (s *Service) Templates() []TemplateInfo {
	if s.miner == nil {
		return nil
	}

	markings := map[string]detect.Marking{}
	if s.logDetector != nil {
		markings = s.logDetector.Markings()
	}

	templates := []TemplateInfo{}

	for _, partition := range s.miner.Partitions() {
		miner, err := s.miner.Partition(partition)
		if err != nil {
			continue
		}

		for _, cluster := range miner.Clusters() {
			template := cluster.Template()

			templates = append(templates, TemplateInfo{
				Partition: partition,
				ID:        cluster.ID,
				Template:  template,
				Size:      cluster.Size,
				Marking:   markings[template],
			})
		}
	}

	sort.Slice(templates, func(i, j int) bool {
		if templates[i].Partition != templates[j].Partition {
			return templates[i].Partition < templates[j].Partition
		}

		return templates[i].ID < templates[j].ID
	})

	return templates
}
