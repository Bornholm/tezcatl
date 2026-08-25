package docker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func statsFixture(totalUsage uint64, systemUsage uint64) string {
	return fmt.Sprintf(`{
		"cpu_stats": {
			"cpu_usage": {"total_usage": %d},
			"system_cpu_usage": %d,
			"online_cpus": 2
		},
		"memory_stats": {
			"usage": 600000000,
			"limit": 1000000000,
			"stats": {"inactive_file": 100000000}
		}
	}`, totalUsage, systemUsage)
}

// fakeDaemon serves a minimal Docker Engine API over a unix socket.
func fakeDaemon(t *testing.T) (socket string, bump func()) {
	t.Helper()

	socket = filepath.Join(t.TempDir(), "docker.sock")

	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
	t.Cleanup(func() { listener.Close() })

	// Each poll advances the CPU counters: +100 usage over +400 system.
	poll := uint64(0)

	mux := http.NewServeMux()

	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
			{"Id": "aaa111", "Names": ["/checkout.web.1"], "Labels": {"com.dokku.app-name": "checkout"}},
			{"Id": "bbb222", "Names": ["/checkout.web.2"], "Labels": {"com.dokku.app-name": "checkout"}},
			{"Id": "ccc333", "Names": ["/standalone-container"], "Labels": {}}
		]`)
	})

	mux.HandleFunc("/containers/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, statsFixture(1000+poll*100, 10000+poll*400))
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })

	return socket, func() { poll++ }
}

func TestCollector(t *testing.T) {
	socket, bump := fakeDaemon(t)

	collector, err := NewCollector(&Options{
		Socket:      socket,
		Environment: "production",
	})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	out := make(chan model.Observation, 64)
	ctx := context.Background()

	collect := func() []model.Observation {
		if err := collector.poll(ctx, out); err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}

		observations := []model.Observation{}
		for {
			select {
			case obs := <-out:
				observations = append(observations, obs)
			default:
				return observations
			}
		}
	}

	first := collect()

	// First poll: no CPU baseline, but memory and running counts.
	for _, obs := range first {
		if obs.Metric.Name == MetricCPUPercent {
			t.Fatalf("expected no cpu sample on first poll, got %+v", obs.Metric)
		}
	}

	bump()
	second := collect()

	byKey := map[string]model.Observation{}
	for _, obs := range second {
		byKey[obs.Service+"/"+obs.Metric.Name+"/"+obs.Metric.Labels["container"]] = obs
	}

	// CPU: delta usage 100 over delta system 400, 2 CPUs → 50%.
	cpu, exists := byKey["checkout/docker.cpu.percent/checkout.web.1"]
	if !exists || cpu.Metric.Value < 49 || cpu.Metric.Value > 51 {
		t.Fatalf("expected ~50%% cpu for checkout.web.1, got %+v", cpu.Metric)
	}

	// Memory: (600M - 100M cache) / 1G → 50%.
	memory := byKey["checkout/docker.memory.used_percent/checkout.web.1"]
	if memory.Metric.Value != 50 {
		t.Errorf("expected 50%% memory, got %+v", memory.Metric)
	}

	// Identity: dokku label wins, container name fallback otherwise.
	if _, exists := byKey["standalone-container/docker.cpu.percent/standalone-container"]; !exists {
		t.Errorf("expected container name fallback identity, got %v", byKey)
	}

	// Running containers per service.
	running := byKey["checkout/docker.containers.running/"]
	if running.Metric.Value != 2 {
		t.Errorf("expected 2 running containers for checkout, got %+v", running.Metric)
	}

	if running.Environment != "production" {
		t.Errorf("unexpected environment: %q", running.Environment)
	}
}
