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
)

// BenchmarkPipelineThroughput measures the full standalone pipeline:
// normalization, template mining, detection and correlation.
func BenchmarkPipelineThroughput(b *testing.B) {
	var corpus strings.Builder
	for i := range 10000 {
		fmt.Fprintf(&corpus, "GET /api/users/%d returned %d in %d ms\n", i, 200+(i%3)*100, i%300)
	}

	lines := corpus.String()

	b.ReportAllocs()
	b.SetBytes(int64(len(lines)))

	for b.Loop() {
		cfg := config.Default()
		cfg.Logs.Detection.LearningPeriod = 0
		cfg.Pipeline.FlushInterval = config.Duration(100 * time.Millisecond)

		runtime, err := NewRuntime(context.Background(), cfg, WithEventsOutput(io.Discard))
		if err != nil {
			b.Fatalf("unexpected error: %+v", err)
		}

		if err := runtime.Run(context.Background(), stdio.NewLogIngester(strings.NewReader(lines), stdio.Identity{Service: "bench"})); err != nil {
			b.Fatalf("unexpected error: %+v", err)
		}
	}
}
