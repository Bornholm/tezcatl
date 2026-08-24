package drain

import (
	"fmt"
	"testing"
)

func BenchmarkTemplateMinerAddLogMessage(b *testing.B) {
	miner, err := NewTemplateMiner(testConfig())
	if err != nil {
		b.Fatalf("unexpected error: %+v", err)
	}

	messages := make([]string, 1000)
	for i := range messages {
		switch i % 4 {
		case 0:
			messages[i] = fmt.Sprintf("Accepted password for user%d from 10.0.%d.%d port %d ssh2", i%50, i%3, i%250, 20000+i)
		case 1:
			messages[i] = fmt.Sprintf("GET /api/users/%d returned 200 in %d ms", i, i%300)
		case 2:
			messages[i] = fmt.Sprintf("connection pool db-main usage %d percent", i%100)
		default:
			messages[i] = fmt.Sprintf("session %d expired for user user%d", 1000+i, i%50)
		}
	}

	b.ReportAllocs()

	for i := 0; b.Loop(); i++ {
		miner.AddLogMessage(messages[i%len(messages)])
	}
}

func BenchmarkTemplateMinerMatch(b *testing.B) {
	miner, err := NewTemplateMiner(testConfig())
	if err != nil {
		b.Fatalf("unexpected error: %+v", err)
	}

	for i := range 1000 {
		miner.AddLogMessage(fmt.Sprintf("GET /api/users/%d returned 200 in %d ms", i, i%300))
	}

	b.ReportAllocs()

	for b.Loop() {
		miner.Match("GET /api/users/42 returned 200 in 13 ms", SearchStrategyNever)
	}
}
