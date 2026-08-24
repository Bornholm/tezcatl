package grpc

import (
	"context"
	"log/slog"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second
)

// Client forwards observations produced by a local ingester to a remote
// tezcatl server. On stream failure it reconnects with an exponential
// backoff; the observation being sent is retried on the new stream,
// already-acknowledged ones are not resent.
type Client struct {
	target string
}

func NewClient(target string) *Client {
	return &Client{target: target}
}

// Forward drains the observations channel into the remote server and
// returns once the channel is closed and the stream acknowledged.
func (c *Client) Forward(ctx context.Context, observations <-chan model.Observation) error {
	dialTarget, err := DialTarget(c.target)
	if err != nil {
		return errors.WithStack(err)
	}

	conn, err := grpc.NewClient(dialTarget, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return errors.WithStack(err)
	}
	defer conn.Close()

	client := tezcatlv1.NewIngestServiceClient(conn)

	var (
		stream  tezcatlv1.IngestService_StreamObservationsClient
		backoff = initialBackoff
	)

	closeStream := func() (uint64, error) {
		if stream == nil {
			return 0, nil
		}

		summary, err := stream.CloseAndRecv()
		stream = nil

		if err != nil {
			return 0, errors.WithStack(err)
		}

		return summary.GetAccepted(), nil
	}

	for obs := range observations {
		proto := toProtoObservation(&obs)

		for {
			if stream == nil {
				stream, err = client.StreamObservations(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return errors.WithStack(ctx.Err())
					}

					slog.WarnContext(ctx, "could not open stream, retrying", slog.String("target", c.target), slog.Duration("backoff", backoff), slog.Any("error", err))

					select {
					case <-time.After(backoff):
					case <-ctx.Done():
						return errors.WithStack(ctx.Err())
					}

					backoff = min(backoff*2, maxBackoff)

					continue
				}

				backoff = initialBackoff
			}

			if err := stream.Send(proto); err != nil {
				// The actual failure is reported by CloseAndRecv, which
				// also releases the broken stream.
				_, closeErr := closeStream()
				slog.WarnContext(ctx, "stream failed, reconnecting", slog.String("target", c.target), slog.Any("error", closeErr))

				continue
			}

			break
		}
	}

	accepted, err := closeStream()
	if err != nil {
		return errors.WithStack(err)
	}

	slog.InfoContext(ctx, "stream closed", slog.Uint64("accepted", accepted))

	return nil
}
