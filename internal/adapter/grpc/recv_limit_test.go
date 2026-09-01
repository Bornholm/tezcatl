package grpc

import (
	"context"
	"net"
	"strings"
	"testing"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"google.golang.org/grpc"
)

type hugeAdmin struct {
	tezcatlv1.UnimplementedAdminServiceServer
}

// ListEvents answers with roughly 8 MB, twice the gRPC client default
// that used to break `incidents` and itztli on two weeks of retention.
func (hugeAdmin) ListEvents(ctx context.Context, req *tezcatlv1.ListEventsRequest) (*tezcatlv1.ListEventsResponse, error) {
	event := strings.Repeat("x", 8*1024)

	res := &tezcatlv1.ListEventsResponse{}
	for range 1024 {
		res.Events = append(res.Events, &tezcatlv1.EventEnvelope{Json: event})
	}

	return res, nil
}

func TestDialAcceptsResponsesBeyondTheGRPCDefault(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	// MaxSendMsgSize defaults to unlimited server-side, like the real
	// server: the 4 MB ceiling this test guards against is the
	// client's.
	server := grpc.NewServer()
	tezcatlv1.RegisterAdminServiceServer(server, hugeAdmin{})
	go server.Serve(listener)
	t.Cleanup(server.Stop)

	conn, err := Dial("tcp://"+listener.Addr().String(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	res, err := tezcatlv1.NewAdminServiceClient(conn).ListEvents(context.Background(), &tezcatlv1.ListEventsRequest{})
	if err != nil {
		t.Fatalf("an 8 MB admin response must go through: %v", err)
	}

	if len(res.GetEvents()) != 1024 {
		t.Fatalf("got %d events, want 1024", len(res.GetEvents()))
	}
}
