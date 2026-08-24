package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"os"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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
	caFile string
}

type ClientOption func(c *Client)

// ClientWithCA sets the PEM CA bundle used to verify tls:// targets.
func ClientWithCA(caFile string) ClientOption {
	return func(c *Client) {
		c.caFile = caFile
	}
}

func NewClient(target string, opts ...ClientOption) *Client {
	c := &Client{target: target}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Dial opens a gRPC connection to a tezcatl target URL. For tls://
// targets, caFile optionally points to a PEM CA bundle (self-signed
// deployments); empty means the system roots.
func Dial(target string, caFile string) (*grpc.ClientConn, error) {
	dialTarget, err := DialTarget(target)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	_, _, secure, err := parseTarget(target)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	transport := insecure.NewCredentials()

	if secure {
		tlsConfig := &tls.Config{}

		if caFile != "" {
			pem, err := os.ReadFile(caFile)
			if err != nil {
				return nil, errors.Wrap(err, "could not read ca file")
			}

			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.Errorf("no certificate found in %q", caFile)
			}

			tlsConfig.RootCAs = pool
		}

		transport = credentials.NewTLS(tlsConfig)
	}

	conn, err := grpc.NewClient(dialTarget, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return conn, nil
}

// Forward drains the observations channel into the remote server and
// returns once the channel is closed and the stream acknowledged.
func (c *Client) Forward(ctx context.Context, observations <-chan model.Observation) error {
	conn, err := Dial(c.target, c.caFile)
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
