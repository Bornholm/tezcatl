package setup

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/adapter/stdio"
	"github.com/bornholm/tezcatl/internal/config"
	"github.com/bornholm/tezcatl/internal/core/drain"
)

// BenchmarkPipelineThroughput measures the full standalone pipeline
// under realistic conditions: parsing, the standard masking set
// (IP/HEX/NUM), template mining, detection and correlation.
func BenchmarkPipelineThroughput(b *testing.B) {
	var corpus strings.Builder
	for i := range 10000 {
		fmt.Fprintf(&corpus, "GET /api/users/%d from 10.0.%d.%d returned %d in %d ms\n", i, i%3, i%250, 200+(i%3)*100, i%300)
	}

	lines := corpus.String()

	b.ReportAllocs()
	b.SetBytes(int64(len(lines)))

	for b.Loop() {
		cfg := config.Default()
		cfg.Logs.Detection.LearningPeriod = 0
		cfg.Pipeline.FlushInterval = config.Duration(100 * time.Millisecond)
		cfg.Logs.Drain.Masking = []drain.MaskingInstruction{
			{Pattern: `\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`, MaskWith: "IP"},
			{Pattern: `\b[0-9a-fA-F]{8,}\b`, MaskWith: "HEX"},
			{Pattern: `\b\d+\b`, MaskWith: "NUM"},
		}

		runtime, err := NewRuntime(context.Background(), cfg, WithEventsOutput(io.Discard))
		if err != nil {
			b.Fatalf("unexpected error: %+v", err)
		}

		if err := runtime.Run(context.Background(), stdio.NewLogIngester(strings.NewReader(lines), stdio.Identity{Service: "bench"})); err != nil {
			b.Fatalf("unexpected error: %+v", err)
		}
	}
}
