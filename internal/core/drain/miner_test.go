package drain

import (
	"fmt"
	"testing"
)

func testConfig() *Config {
	config := DefaultConfig()
	config.Masking = []MaskingInstruction{
		{Pattern: `\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`, MaskWith: "IP"},
		{Pattern: `\b\d+\b`, MaskWith: "NUM"},
	}

	return config
}

func TestTemplateMinerClustering(t *testing.T) {
	miner, err := NewTemplateMiner(testConfig())
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	first := miner.AddLogMessage("connected to 10.0.0.1")
	if first.ChangeType != ChangeTypeClusterCreated {
		t.Fatalf("expected cluster_created, got %s", first.ChangeType)
	}

	second := miner.AddLogMessage("connected to 10.0.0.2")
	if second.ChangeType != ChangeTypeNone {
		t.Fatalf("expected none, got %s", second.ChangeType)
	}

	if first.Cluster.ID != second.Cluster.ID {
		t.Fatalf("expected same cluster, got %d and %d", first.Cluster.ID, second.Cluster.ID)
	}

	if template := second.Cluster.Template(); template != "connected to <IP>" {
		t.Fatalf("unexpected template: %q", template)
	}

	third := miner.AddLogMessage("user alice logged in")
	fourth := miner.AddLogMessage("user bob logged in")

	if third.Cluster.ID != fourth.Cluster.ID {
		t.Fatal("expected user messages to share a cluster")
	}

	if fourth.ChangeType != ChangeTypeClusterTemplateChanged {
		t.Fatalf("expected cluster_template_changed, got %s", fourth.ChangeType)
	}

	if template := fourth.Cluster.Template(); template != "user <*> logged in" {
		t.Fatalf("unexpected template: %q", template)
	}

	if size := fourth.Cluster.Size; size != 2 {
		t.Fatalf("expected cluster size 2, got %d", size)
	}
}

func TestTemplateMinerMatch(t *testing.T) {
	miner, err := NewTemplateMiner(testConfig())
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	miner.AddLogMessage("user alice logged in")
	learned := miner.AddLogMessage("user bob logged in")

	matched := miner.Match("user carol logged in", SearchStrategyNever)
	if matched == nil || matched.ID != learned.Cluster.ID {
		t.Fatalf("expected match on cluster %d, got %+v", learned.Cluster.ID, matched)
	}

	if size := matched.Size; size != 2 {
		t.Fatalf("expected match to not increase cluster size, got %d", size)
	}

	if miner.Match("unknown message shape", SearchStrategyNever) != nil {
		t.Fatal("expected no match for unknown message")
	}

	if miner.Match("user dave logged out", SearchStrategyFallback) != nil {
		t.Fatal("expected no match for partially similar message")
	}
}

func TestTemplateMinerExtractParameters(t *testing.T) {
	miner, err := NewTemplateMiner(testConfig())
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	miner.AddLogMessage("user alice connected from 10.0.0.1")
	result := miner.AddLogMessage("user bob connected from 10.0.0.2")

	parameters := miner.ExtractParameters(result.Cluster, "user carol connected from 192.168.1.10")

	if len(parameters) != 2 || parameters[0] != "carol" || parameters[1] != "<IP>" {
		t.Fatalf("unexpected parameters: %+v", parameters)
	}
}

func TestTemplateMinerLRUEviction(t *testing.T) {
	config := testConfig()
	config.MaxClusters = 2

	miner, err := NewTemplateMiner(config)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	miner.AddLogMessage("alpha event happened")
	miner.AddLogMessage("beta event happened again")
	miner.AddLogMessage("gamma failure detected somewhere")

	clusters := miner.Clusters()
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters after eviction, got %d", len(clusters))
	}

	// The oldest cluster (alpha) must have been evicted.
	for _, cluster := range clusters {
		if cluster.ID == 1 {
			t.Fatalf("expected cluster 1 to be evicted, got %+v", clusters)
		}
	}

	// The evicted cluster's slot in the tree must not break matching.
	result := miner.AddLogMessage("alpha event happened")
	if result.ChangeType != ChangeTypeClusterCreated {
		t.Fatalf("expected evicted template to be re-learned, got %s", result.ChangeType)
	}
}

func TestTemplateMinerSnapshotRoundTrip(t *testing.T) {
	miner, err := NewTemplateMiner(testConfig())
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	for i := range 50 {
		miner.AddLogMessage(fmt.Sprintf("user user%d logged in", i))
		miner.AddLogMessage(fmt.Sprintf("connected to 10.0.0.%d", i))
		miner.AddLogMessage("startup complete")
	}

	snapshot, err := miner.Snapshot()
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	restored, err := NewTemplateMiner(testConfig())
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	before := miner.Clusters()
	after := restored.Clusters()

	if len(before) != len(after) {
		t.Fatalf("expected %d clusters, got %d", len(before), len(after))
	}

	for i := range before {
		if before[i].ID != after[i].ID || before[i].Template() != after[i].Template() || before[i].Size != after[i].Size {
			t.Fatalf("cluster mismatch at %d: %+v vs %+v", i, before[i], after[i])
		}
	}

	// Learning must resume seamlessly: same message, same cluster.
	original := miner.AddLogMessage("user zed logged in")
	resumed := restored.AddLogMessage("user zed logged in")

	if original.Cluster.ID != resumed.Cluster.ID {
		t.Fatalf("expected same cluster after restore, got %d and %d", original.Cluster.ID, resumed.Cluster.ID)
	}

	// New clusters must not reuse evicted/used ids.
	next := restored.AddLogMessage("something entirely different appears now")
	if next.ChangeType != ChangeTypeClusterCreated || next.Cluster.ID <= original.Cluster.ID {
		t.Fatalf("unexpected cluster id after restore: %+v", next)
	}
}

func TestPartitionedMiner(t *testing.T) {
	partitioned := NewPartitionedMiner(testConfig())

	payments, err := partitioned.Partition("prod/payments")
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	auth, err := partitioned.Partition("prod/auth")
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	payments.AddLogMessage("payment 42 accepted")
	auth.AddLogMessage("login failed for alice")

	if len(payments.Clusters()) != 1 || len(auth.Clusters()) != 1 {
		t.Fatal("expected independent partitions")
	}

	snapshot, err := partitioned.Snapshot()
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	restored := NewPartitionedMiner(testConfig())
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if names := restored.Partitions(); len(names) != 2 {
		t.Fatalf("expected 2 partitions, got %+v", names)
	}
}
