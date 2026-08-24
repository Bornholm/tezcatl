package grpc

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/core/model"
)

// BenchmarkStreamObservations measures the gRPC ingestion transport over
// a unix socket: client-streaming send, server-side decoding and hand-off
// to the pipeline channel.
func BenchmarkStreamObservations(b *testing.B) {
	target := fmt.Sprintf("unix://%s", filepath.Join(b.TempDir(), "bench.sock"))

	received := make(chan model.Observation, 4096)

	server := NewServerIngester([]string{target})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Ingest(ctx, received)

	// Drain the pipeline side so the server never blocks.
	go func() {
		for range received {
		}
	}()

	time.Sleep(100 * time.Millisecond)

	conn, err := Dial(target, "")
	if err != nil {
		b.Fatalf("unexpected error: %+v", err)
	}
	defer conn.Close()

	stream, err := tezcatlv1.NewIngestServiceClient(conn).StreamObservations(ctx)
	if err != nil {
		b.Fatalf("unexpected error: %+v", err)
	}

	obs := &model.Observation{
		ID:          model.NewID(),
		Service:     "bench",
		Environment: "prod",
		Modality:    model.ModalityLog,
		Timestamp:   time.Now(),
		Log:         &model.LogRecord{Raw: "GET /api/users/42 returned 200 in 13 ms"},
	}

	proto := toProtoObservation(obs)

	b.ReportAllocs()
	b.SetBytes(int64(len(obs.Log.Raw)))
	b.ResetTimer()

	for b.Loop() {
		if err := stream.Send(proto); err != nil {
			b.Fatalf("unexpected error: %+v", err)
		}
	}

	b.StopTimer()

	if _, err := stream.CloseAndRecv(); err != nil {
		b.Fatalf("unexpected error: %+v", err)
	}
}
