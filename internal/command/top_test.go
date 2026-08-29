package command

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	adaptergrpc "github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/sink"
	"google.golang.org/grpc"
)

// TestEventsReportsServerDeath is the case a terminal session got wrong:
// once the stream is established, the client must notice the server
// going away instead of claiming the feed is still live.
func TestEventsReportsServerDeath(t *testing.T) {
	target := fmt.Sprintf("unix://%s", filepath.Join(t.TempDir(), "admin.sock"))

	broadcast := sink.NewBroadcast(10)
	defer broadcast.Close()

	listener, err := adaptergrpc.Listen(target, nil)
	if err != nil {
		t.Fatalf("could not listen: %+v", err)
	}

	server := grpc.NewServer()
	adaptergrpc.NewAdminServer(admin.NewService(nil, nil, nil, admin.WithEventStream(broadcast))).Register(server)

	go func() {
		_ = server.Serve(listener)
	}()

	conn, err := adaptergrpc.Dial(target, "")
	if err != nil {
		t.Fatalf("could not dial: %+v", err)
	}

	defer conn.Close()

	source := &grpcSource{client: tezcatlv1.NewAdminServiceClient(conn)}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan model.Event, 8)
	connected := make(chan struct{})

	returned := make(chan error, 1)
	go func() {
		returned <- source.Events(ctx, 0, events, func() { close(connected) })
	}()

	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never reported itself connected")
	}

	if err := broadcast.Publish(ctx, []model.Event{{ID: "e1", Kind: "anomaly.log", Timestamp: time.Now()}}); err != nil {
		t.Fatalf("could not publish: %+v", err)
	}

	select {
	case event := <-events:
		if event.ID != "e1" {
			t.Errorf("expected event e1, got %s", event.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the published event never arrived")
	}

	// The server goes away under the client.
	server.Stop()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Events kept waiting after the server went away")
	}
}
