package drain

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"

	"github.com/pkg/errors"
)

const snapshotVersion = 1

type snapshot struct {
	Version  int               `json:"version"`
	Counter  int64             `json:"counter"`
	Clusters []snapshotCluster `json:"clusters"`
}

type snapshotCluster struct {
	ID             int64    `json:"id"`
	TemplateTokens []string `json:"template_tokens"`
	Size           int64    `json:"size"`
}

// Snapshot serializes the miner state as gzipped JSON. The prefix tree is
// not serialized: it is deterministically rebuilt from the clusters on
// restore.
func (m *TemplateMiner) Snapshot() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	snap := snapshot{
		Version: snapshotVersion,
		Counter: m.drain.clustersCounter,
	}

	// Least to most recently used, so restoring preserves the LRU order.
	for _, cluster := range m.drain.Clusters() {
		snap.Clusters = append(snap.Clusters, snapshotCluster{
			ID:             cluster.ID,
			TemplateTokens: cluster.TemplateTokens,
			Size:           cluster.Size,
		})
	}

	var buf bytes.Buffer

	writer := gzip.NewWriter(&buf)
	if err := json.NewEncoder(writer).Encode(snap); err != nil {
		return nil, errors.WithStack(err)
	}

	if err := writer.Close(); err != nil {
		return nil, errors.WithStack(err)
	}

	return buf.Bytes(), nil
}

// Restore replaces the miner state with a snapshot previously produced by
// Snapshot.
func (m *TemplateMiner) Restore(data []byte) error {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return errors.WithStack(err)
	}

	decoded, err := io.ReadAll(reader)
	if err != nil {
		return errors.WithStack(err)
	}

	var snap snapshot
	if err := json.Unmarshal(decoded, &snap); err != nil {
		return errors.WithStack(err)
	}

	if snap.Version != snapshotVersion {
		return errors.Errorf("unsupported snapshot version %d", snap.Version)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	drain := NewDrain(m.config)
	drain.clustersCounter = snap.Counter

	for _, snapCluster := range snap.Clusters {
		cluster := &Cluster{
			ID:             snapCluster.ID,
			TemplateTokens: snapCluster.TemplateTokens,
			Size:           snapCluster.Size,
		}

		drain.clusters.Put(cluster.ID, cluster)
		drain.addSeqToPrefixTree(cluster)
	}

	m.drain = drain

	return nil
}
