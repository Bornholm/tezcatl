package drain

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"testing"
)

type golden struct {
	Lines []struct {
		ClusterID  int64  `json:"cluster_id"`
		ChangeType string `json:"change_type"`
		Template   string `json:"template"`
	} `json:"lines"`
	Clusters []struct {
		ID       int64  `json:"id"`
		Template string `json:"template"`
		Size     int64  `json:"size"`
	} `json:"clusters"`
}

// goldenConfig mirrors the configuration used by
// misc/drain3-golden/generate.py. Keep both in sync.
func goldenConfig() *Config {
	parametrize := true

	return &Config{
		Depth:                    4,
		SimTh:                    0.4,
		MaxChildren:              100,
		ParamStr:                 "<*>",
		ParametrizeNumericTokens: &parametrize,
		MaskPrefix:               "<",
		MaskSuffix:               ">",
		Masking: []MaskingInstruction{
			{Pattern: `\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`, MaskWith: "IP"},
			{Pattern: `\b\d+\b`, MaskWith: "NUM"},
		},
	}
}

// TestGoldenDrain3Compatibility replays the corpus mined by the official
// drain3 Python implementation (see misc/drain3-golden/generate.py) and
// requires line-by-line identical clustering decisions.
func TestGoldenDrain3Compatibility(t *testing.T) {
	runGolden(t, "testdata/golden.json", goldenConfig())
}

// TestGoldenDrain3LRUCompatibility does the same with a tight cluster
// limit, exercising LRU eviction and the lazy cleanup of evicted ids in
// the prefix tree.
func TestGoldenDrain3LRUCompatibility(t *testing.T) {
	config := goldenConfig()
	config.MaxClusters = 4

	runGolden(t, "testdata/golden_lru.json", config)
}

func runGolden(t *testing.T, fixturePath string, config *Config) {
	t.Helper()

	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("could not read fixture: %+v", err)
	}

	var expected golden
	if err := json.Unmarshal(fixture, &expected); err != nil {
		t.Fatalf("could not parse fixture: %+v", err)
	}

	corpus, err := os.Open("testdata/corpus.log")
	if err != nil {
		t.Fatalf("could not read corpus: %+v", err)
	}
	defer corpus.Close()

	miner, err := NewTemplateMiner(config)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	scanner := bufio.NewScanner(corpus)
	index := 0

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		if index >= len(expected.Lines) {
			t.Fatalf("corpus has more lines than the fixture (%d)", len(expected.Lines))
		}

		want := expected.Lines[index]
		result := miner.AddLogMessage(line)

		if result.Cluster.ID != want.ClusterID {
			t.Fatalf("line %d %q: expected cluster %d, got %d", index+1, line, want.ClusterID, result.Cluster.ID)
		}

		if string(result.ChangeType) != want.ChangeType {
			t.Fatalf("line %d %q: expected change type %q, got %q", index+1, line, want.ChangeType, result.ChangeType)
		}

		if template := result.Cluster.Template(); template != want.Template {
			t.Fatalf("line %d %q: expected template %q, got %q", index+1, line, want.Template, template)
		}

		index++
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("could not read corpus: %+v", err)
	}

	if index != len(expected.Lines) {
		t.Fatalf("corpus has fewer lines (%d) than the fixture (%d)", index, len(expected.Lines))
	}

	clusters := miner.Clusters()
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].ID < clusters[j].ID })

	if len(clusters) != len(expected.Clusters) {
		t.Fatalf("expected %d clusters, got %d", len(expected.Clusters), len(clusters))
	}

	for i, want := range expected.Clusters {
		cluster := clusters[i]
		if cluster.ID != want.ID || cluster.Template() != want.Template || cluster.Size != want.Size {
			t.Fatalf("cluster mismatch: expected {%d %q %d}, got {%d %q %d}",
				want.ID, want.Template, want.Size, cluster.ID, cluster.Template(), cluster.Size)
		}
	}
}
