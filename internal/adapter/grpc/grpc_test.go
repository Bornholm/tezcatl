package grpc

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"golang.org/x/sync/errgroup"
)

func TestClientServerRoundTrip(t *testing.T) {
	target := fmt.Sprintf("unix://%s", filepath.Join(t.TempDir(), "tezcatl.sock"))

	const total = 500

	received := make(chan model.Observation, total)

	server := NewServerIngester([]string{target})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	serverCtx, stopServer := context.WithCancel(ctx)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		err := server.Ingest(serverCtx, received)
		if serverCtx.Err() != nil {
			return nil
		}

		return err
	})

	// Wait for the socket to be up.
	time.Sleep(100 * time.Millisecond)

	observations := make(chan model.Observation, total)
	for i := range total {
		observations <- model.Observation{
			ID:       fmt.Sprintf("obs-%d", i),
			Source:   "api",
			Modality: model.ModalityLog,
			Log:      &model.LogRecord{Raw: fmt.Sprintf("line %d", i)},
			Attributes: map[string]string{
				"index": fmt.Sprintf("%d", i),
			},
		}
	}
	close(observations)

	client := NewClient(target)

	g.Go(func() error {
		defer stopServer()

		return client.Forward(gctx, observations)
	})

	var mu sync.Mutex
	collected := map[string]model.Observation{}

	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)

		for obs := range received {
			mu.Lock()
			collected[obs.ID] = obs
			mu.Unlock()

			if len(collected) == total {
				return
			}
		}
	}()

	if err := g.Wait(); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	select {
	case <-collectDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("expected %d observations, got %d", total, len(collected))
	}

	obs, exists := collected["obs-42"]
	if !exists {
		t.Fatal("missing observation obs-42")
	}

	if obs.Source != "api" || obs.Modality != model.ModalityLog || obs.Log == nil || obs.Log.Raw != "line 42" {
		t.Fatalf("mangled observation: %+v", obs)
	}

	if obs.IngestedAt.IsZero() {
		t.Fatal("expected ingestion timestamp to be set server-side")
	}
}

func TestParseTarget(t *testing.T) {
	cases := []struct {
		target  string
		network string
		address string
		invalid bool
	}{
		{target: "unix:///run/tezcatl.sock", network: "unix", address: "/run/tezcatl.sock"},
		{target: "tcp://127.0.0.1:4242", network: "tcp", address: "127.0.0.1:4242"},
		{target: "http://example.net", invalid: true},
		{target: "unix://", invalid: true},
	}

	for _, tc := range cases {
		network, address, err := parseTarget(tc.target)

		if tc.invalid {
			if err == nil {
				t.Errorf("expected error for target %q", tc.target)
			}

			continue
		}

		if err != nil {
			t.Errorf("unexpected error for target %q: %+v", tc.target, err)
			continue
		}

		if network != tc.network || address != tc.address {
			t.Errorf("expected %s/%s for target %q, got %s/%s", tc.network, tc.address, tc.target, network, address)
		}
	}
}
