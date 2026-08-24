package processor

import (
	"context"
	"strconv"

	"github.com/bornholm/tezcatl/internal/core/drain"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/pkg/errors"
)

// TemplateMining annotates log observations with the template mined by
// the drain3-compatible miner. Each source has its own independent
// partition; the engine guarantees a partition is processed sequentially.
type TemplateMining struct {
	miner *drain.PartitionedMiner
}

func NewTemplateMining(miner *drain.PartitionedMiner) *TemplateMining {
	return &TemplateMining{miner: miner}
}

func (p *TemplateMining) Name() string {
	return "template-mining"
}

func (p *TemplateMining) Process(ctx context.Context, obs *model.Observation, emit port.EmitFunc) (bool, error) {
	if obs.Modality != model.ModalityLog || obs.Log == nil {
		return true, nil
	}

	miner, err := p.miner.Partition(obs.Source)
	if err != nil {
		return false, errors.WithStack(err)
	}

	result := miner.AddLogMessage(obs.Log.Raw)

	obs.Log.TemplateID = strconv.FormatInt(result.Cluster.ID, 10)
	obs.Log.Template = result.Cluster.Template()
	obs.Log.Parameters = miner.ExtractParameters(result.Cluster, obs.Log.Raw)

	if obs.Attributes == nil {
		obs.Attributes = map[string]string{}
	}

	obs.Attributes[model.AttrTemplateChangeType] = string(result.ChangeType)
	obs.Attributes[model.AttrClusterSize] = strconv.FormatInt(result.Cluster.Size, 10)

	return true, nil
}

// SnapshotKey implements port.Snapshotter through the underlying miner.
func (p *TemplateMining) SnapshotKey() string {
	return "drain"
}

func (p *TemplateMining) Snapshot() ([]byte, error) {
	data, err := p.miner.Snapshot()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return data, nil
}

func (p *TemplateMining) Restore(data []byte) error {
	if err := p.miner.Restore(data); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
