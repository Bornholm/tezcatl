#!/usr/bin/env python3
"""Generate golden fixtures for the Go Drain3 port.

Runs the official drain3 Python implementation over the shared corpus and
records, for every line, the resulting cluster id, change type and
template, plus the final cluster list. The Go test replays the corpus and
must produce identical results.

Usage:
    python3 misc/drain3-golden/generate.py \
        internal/core/drain/testdata/corpus.log \
        > internal/core/drain/testdata/golden.json

Requires: pip install drain3
"""

import json
import sys

from drain3 import TemplateMiner
from drain3.masking import MaskingInstruction
from drain3.template_miner_config import TemplateMinerConfig

# Keep in sync with goldenConfig in golden_test.go.
MASKING = [
    (r"\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b", "IP"),
    (r"\b\d+\b", "NUM"),
]


def build_miner(max_clusters: int | None) -> TemplateMiner:
    config = TemplateMinerConfig()
    config.drain_depth = 4
    config.drain_sim_th = 0.4
    config.drain_max_children = 100
    config.drain_max_clusters = max_clusters
    config.drain_extra_delimiters = []
    config.parametrize_numeric_tokens = True
    config.mask_prefix = "<"
    config.mask_suffix = ">"
    config.masking_instructions = [
        MaskingInstruction(pattern, mask_with) for pattern, mask_with in MASKING
    ]
    config.profiling_enabled = False

    return TemplateMiner(config=config)


def main() -> None:
    corpus_path = sys.argv[1]
    max_clusters = int(sys.argv[2]) if len(sys.argv) > 2 else None

    miner = build_miner(max_clusters)

    lines = []
    with open(corpus_path, encoding="utf-8") as corpus:
        for line in corpus:
            line = line.rstrip("\n")
            if not line:
                continue

            result = miner.add_log_message(line)
            lines.append(
                {
                    "cluster_id": result["cluster_id"],
                    "change_type": result["change_type"],
                    "template": result["template_mined"],
                }
            )

    clusters = [
        {
            "id": cluster.cluster_id,
            "template": cluster.get_template(),
            "size": cluster.size,
        }
        for cluster in sorted(miner.drain.clusters, key=lambda c: c.cluster_id)
    ]

    json.dump({"lines": lines, "clusters": clusters}, sys.stdout, indent=2)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
