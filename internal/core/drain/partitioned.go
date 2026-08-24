package drain

import (
	"encoding/json"
	"maps"
	"sort"
	"sync"

	"github.com/pkg/errors"
)

// PartitionedMiner maintains one independent TemplateMiner per partition
// (typically one per environment/service). Different partitions can be
// mined in parallel; a given partition is mined sequentially.
type PartitionedMiner struct {
	mu         sync.RWMutex
	config     *Config
	partitions map[string]*TemplateMiner
}

func NewPartitionedMiner(config *Config) *PartitionedMiner {
	return &PartitionedMiner{
		config:     config,
		partitions: map[string]*TemplateMiner{},
	}
}

func (p *PartitionedMiner) Partition(name string) (*TemplateMiner, error) {
	p.mu.RLock()
	miner, exists := p.partitions[name]
	p.mu.RUnlock()

	if exists {
		return miner, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if miner, exists := p.partitions[name]; exists {
		return miner, nil
	}

	miner, err := NewTemplateMiner(p.config)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	p.partitions[name] = miner

	return miner, nil
}

func (p *PartitionedMiner) Partitions() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	names := make([]string, 0, len(p.partitions))
	for name := range p.partitions {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// Snapshot serializes every partition, each with its own gzipped
// snapshot.
func (p *PartitionedMiner) Snapshot() ([]byte, error) {
	p.mu.RLock()
	miners := maps.Clone(p.partitions)
	p.mu.RUnlock()

	partitions := map[string][]byte{}
	for name, miner := range miners {
		snap, err := miner.Snapshot()
		if err != nil {
			return nil, errors.Wrapf(err, "could not snapshot partition %q", name)
		}

		partitions[name] = snap
	}

	data, err := json.Marshal(partitions)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return data, nil
}

func (p *PartitionedMiner) Restore(data []byte) error {
	var partitions map[string][]byte
	if err := json.Unmarshal(data, &partitions); err != nil {
		return errors.WithStack(err)
	}

	restored := map[string]*TemplateMiner{}
	for name, snap := range partitions {
		miner, err := NewTemplateMiner(p.config)
		if err != nil {
			return errors.WithStack(err)
		}

		if err := miner.Restore(snap); err != nil {
			return errors.Wrapf(err, "could not restore partition %q", name)
		}

		restored[name] = miner
	}

	p.mu.Lock()
	p.partitions = restored
	p.mu.Unlock()

	return nil
}
