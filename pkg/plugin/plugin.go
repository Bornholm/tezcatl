// Package plugin is the SDK for tezcatl ingestion source plugins.
//
// A source plugin is a standalone binary (hashicorp/go-plugin
// subprocess) that streams observations to tezcatl. A minimal plugin:
//
//	func main() {
//		plugin.Serve(plugin.SourceFunc(func(ctx context.Context, config []byte, emit plugin.EmitFunc) error {
//			return emit(&tezcatlv1.Observation{ /* … */ })
//		}))
//	}
//
// Plugin binaries are named tezcatl-source-<name> and installed in the
// plugins directory of the host (tezcatl plugin install).
package plugin

import (
	"context"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
)

// PluginName is the key of the source plugin in the go-plugin map.
const PluginName = "source"

// Handshake is shared between the tezcatl host and plugin binaries.
// ProtocolVersion must be incremented whenever the gRPC contract
// changes incompatibly.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "TEZCATL_PLUGIN",
	MagicCookieValue: "tezcatl-source-v1",
}

// EmitFunc pushes one observation to the host. It returns an error when
// the host stream is gone; the plugin should then stop.
type EmitFunc func(obs *tezcatlv1.Observation) error

// Source is implemented by plugin authors: stream observations until
// the context is canceled. config is the plugin-specific configuration
// as JSON, coming from the tezcatl configuration or CLI.
type Source interface {
	Stream(ctx context.Context, config []byte, emit EmitFunc) error
}

// SourceFunc adapts a function to the Source interface.
type SourceFunc func(ctx context.Context, config []byte, emit EmitFunc) error

func (f SourceFunc) Stream(ctx context.Context, config []byte, emit EmitFunc) error {
	return f(ctx, config, emit)
}

// GRPCPlugin implements goplugin.GRPCPlugin for the source contract.
// Impl is only set on the plugin binary side.
type GRPCPlugin struct {
	goplugin.Plugin
	Impl Source
}

func (p *GRPCPlugin) GRPCServer(broker *goplugin.GRPCBroker, s *grpc.Server) error {
	tezcatlv1.RegisterSourceServiceServer(s, &sourceServer{impl: p.Impl})
	return nil
}

func (p *GRPCPlugin) GRPCClient(ctx context.Context, broker *goplugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return tezcatlv1.NewSourceServiceClient(c), nil
}

type sourceServer struct {
	tezcatlv1.UnimplementedSourceServiceServer

	impl Source
}

func (s *sourceServer) Stream(req *tezcatlv1.SourceStreamRequest, stream tezcatlv1.SourceService_StreamServer) error {
	return s.impl.Stream(stream.Context(), req.GetConfig(), stream.Send)
}

// Serve starts the plugin. Call it from the plugin binary's main().
func Serve(source Source) {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins: map[string]goplugin.Plugin{
			PluginName: &GRPCPlugin{Impl: source},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
	})
}
