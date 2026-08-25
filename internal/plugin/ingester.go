package plugin

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	grpcadapter "github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/core/model"
	sdk "github.com/bornholm/tezcatl/pkg/plugin"
	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/pkg/errors"
)

const (
	restartInitialBackoff = time.Second
	restartMaxBackoff     = time.Minute
)

// SourceIngester runs a source plugin binary as a subprocess and feeds
// its observation stream into the engine. It implements port.Ingester;
// a crashing plugin is restarted with an exponential backoff instead of
// taking the pipeline down.
type SourceIngester struct {
	name   string
	path   string
	config []byte
	now    func() time.Time
}

// NewSourceIngester runs the plugin binary at path; config is the
// plugin-specific configuration as JSON.
func NewSourceIngester(name string, path string, config []byte) *SourceIngester {
	return &SourceIngester{
		name:   name,
		path:   path,
		config: config,
		now:    time.Now,
	}
}

func (i *SourceIngester) Ingest(ctx context.Context, out chan<- model.Observation) error {
	backoff := restartInitialBackoff

	for {
		start := i.now()

		err := i.stream(ctx, out)

		if ctx.Err() != nil {
			return errors.WithStack(ctx.Err())
		}

		// A run that lasted a while resets the backoff.
		if i.now().Sub(start) > restartMaxBackoff {
			backoff = restartInitialBackoff
		}

		slog.ErrorContext(ctx, "source plugin stopped, restarting", slog.String("plugin", i.name), slog.Duration("backoff", backoff), slog.Any("error", err))

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		}

		backoff = min(backoff*2, restartMaxBackoff)
	}
}

// stream runs one plugin subprocess until its stream ends or the
// context is canceled.
func (i *SourceIngester) stream(ctx context.Context, out chan<- model.Observation) error {
	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  sdk.Handshake,
		Plugins:          map[string]goplugin.Plugin{sdk.PluginName: &sdk.GRPCPlugin{}},
		Cmd:              exec.Command(i.path),
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		Logger: hclog.New(&hclog.LoggerOptions{
			Name:   "plugin." + i.name,
			Level:  hclog.Info,
			Output: hclogWriter{},
		}),
	})
	defer client.Kill()

	rpc, err := client.Client()
	if err != nil {
		return errors.WithStack(err)
	}

	raw, err := rpc.Dispense(sdk.PluginName)
	if err != nil {
		return errors.WithStack(err)
	}

	source, ok := raw.(tezcatlv1.SourceServiceClient)
	if !ok {
		return errors.Errorf("unexpected plugin client type %T", raw)
	}

	stream, err := source.Stream(ctx, &tezcatlv1.SourceStreamRequest{Config: i.config})
	if err != nil {
		return errors.WithStack(err)
	}

	for {
		proto, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.Errorf("plugin %q closed its stream", i.name)
			}

			return errors.WithStack(err)
		}

		obs := grpcadapter.FromProtoObservation(proto, i.now())

		select {
		case out <- obs:
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		}
	}
}

// hclogWriter routes go-plugin logs to slog.
type hclogWriter struct{}

func (hclogWriter) Write(p []byte) (int, error) {
	slog.Debug(string(p))
	return len(p), nil
}
