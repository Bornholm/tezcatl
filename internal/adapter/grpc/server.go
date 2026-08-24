package grpc

import (
	"context"
	"io"
	"log/slog"
	"net"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

// ServerIngester exposes the IngestService over one or more listeners and
// feeds every received observation into the engine. It implements
// port.Ingester: the gRPC server lives for as long as the engine runs.
type ServerIngester struct {
	tezcatlv1.UnimplementedIngestServiceServer

	targets []string
	now     func() time.Time

	out chan<- model.Observation
	ctx context.Context
}

func NewServerIngester(targets ...string) *ServerIngester {
	return &ServerIngester{
		targets: targets,
		now:     time.Now,
	}
}

func (s *ServerIngester) Ingest(ctx context.Context, out chan<- model.Observation) error {
	if len(s.targets) == 0 {
		return errors.New("no listen target configured")
	}

	listeners := make([]net.Listener, 0, len(s.targets))
	for _, target := range s.targets {
		listener, err := Listen(target)
		if err != nil {
			return errors.WithStack(err)
		}
		defer listener.Close()

		slog.InfoContext(ctx, "listening", slog.String("target", target))

		listeners = append(listeners, listener)
	}

	s.out = out
	s.ctx = ctx

	server := grpc.NewServer()
	tezcatlv1.RegisterIngestServiceServer(server, s)

	g, gctx := errgroup.WithContext(ctx)

	for _, listener := range listeners {
		g.Go(func() error {
			if err := server.Serve(listener); err != nil {
				return errors.WithStack(err)
			}

			return nil
		})
	}

	g.Go(func() error {
		<-gctx.Done()
		server.GracefulStop()

		return nil
	})

	if err := g.Wait(); err != nil && ctx.Err() == nil {
		return errors.WithStack(err)
	}

	return errors.WithStack(ctx.Err())
}

func (s *ServerIngester) StreamObservations(stream tezcatlv1.IngestService_StreamObservationsServer) error {
	var accepted uint64

	for {
		proto, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&tezcatlv1.StreamSummary{Accepted: accepted})
		}

		if err != nil {
			return errors.WithStack(err)
		}

		obs := fromProtoObservation(proto, s.now())

		select {
		case s.out <- obs:
			accepted++
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
}
