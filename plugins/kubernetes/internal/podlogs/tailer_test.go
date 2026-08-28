package podlogs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/plugins/kubernetes/internal/api"
)

const runningPod = `{
	"metadata": {
		"name": "checkout-7d9f8b-abcde",
		"namespace": "prod-ns",
		"labels": {"app": "checkout"},
		"ownerReferences": [{"kind": "ReplicaSet", "name": "checkout-7d9f8b", "controller": true}]
	},
	"spec": {"containers": [{"name": "web"}]},
	"status": {"phase": "Running"}
}`

// fakeAPIServer serves a pods list with one running pod, a watch that
// stays silent, and the pod's log stream (two lines, then open).
func fakeAPIServer(t *testing.T) *api.Client {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") == "true" {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}

		fmt.Fprintf(w, `{"metadata":{"resourceVersion":"1"},"items":[%s]}`, runningPod)
	})

	mux.HandleFunc("/api/v1/namespaces/prod-ns/pods/checkout-7d9f8b-abcde/log", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("follow") != "true" || query.Get("timestamps") != "true" || query.Get("container") != "web" {
			t.Errorf("unexpected log query: %v", query)
		}

		if query.Get("sinceSeconds") == "" {
			t.Errorf("expected a bounded initial attach, got %v", query)
		}

		fmt.Fprintln(w, `2026-08-28T10:00:00.500000000Z {"level":"error","msg":"payment failed"}`)
		fmt.Fprintln(w, "2026-08-28T10:00:01Z plain text line")
		w.(http.Flusher).Flush()

		<-r.Context().Done()
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := api.NewClient(&api.Config{Server: server.URL})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	return client
}

func TestTailer(t *testing.T) {
	client := fakeAPIServer(t)

	tailer, err := NewTailer(&Options{Client: client})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan model.Observation, 16)
	done := make(chan struct{})

	go func() {
		defer close(done)
		tailer.Ingest(ctx, out)
	}()

	collect := func() model.Observation {
		select {
		case obs := <-out:
			return obs
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for an observation")
			return model.Observation{}
		}
	}

	first := collect()
	second := collect()

	cancel()
	<-done

	// Identity: the app label wins, the namespace is the environment.
	if first.Service != "checkout" || first.Environment != "prod-ns" {
		t.Errorf("unexpected identity: %s/%s", first.Environment, first.Service)
	}

	if expected := time.Date(2026, 8, 28, 10, 0, 0, 500000000, time.UTC); !first.Timestamp.Equal(expected) {
		t.Errorf("expected timestamp %s, got %s", expected, first.Timestamp)
	}

	// Raw keeps the timestamp prefix: unwrapping is the pipeline's job.
	if expected := `2026-08-28T10:00:00.500000000Z {"level":"error","msg":"payment failed"}`; first.Log.Raw != expected {
		t.Errorf("unexpected raw line: %q", first.Log.Raw)
	}

	if first.Attributes[AttrPod] != "checkout-7d9f8b-abcde" || first.Attributes[AttrContainer] != "web" {
		t.Errorf("unexpected attributes: %v", first.Attributes)
	}

	if second.Log.Raw != "2026-08-28T10:00:01Z plain text line" {
		t.Errorf("unexpected raw line: %q", second.Log.Raw)
	}
}

func TestServiceIdentity(t *testing.T) {
	tailer, err := NewTailer(&Options{Client: &api.Client{}})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	testCases := []struct {
		name     string
		pod      pod
		expected string
	}{
		{
			name: "canonical label wins over app",
			pod: podWith(api.ObjectMeta{
				Name:   "checkout-7d9f8b-abcde",
				Labels: map[string]string{"app.kubernetes.io/name": "checkout", "app": "other"},
			}),
			expected: "checkout",
		},
		{
			name: "replicaset owner trimmed of its hash",
			pod: podWith(api.ObjectMeta{
				Name:            "checkout-7d9f8b-abcde",
				OwnerReferences: []api.OwnerReference{{Kind: "ReplicaSet", Name: "checkout-7d9f8b", Controller: true}},
			}),
			expected: "checkout",
		},
		{
			name: "statefulset owner name as is",
			pod: podWith(api.ObjectMeta{
				Name:            "postgres-0",
				OwnerReferences: []api.OwnerReference{{Kind: "StatefulSet", Name: "postgres", Controller: true}},
			}),
			expected: "postgres",
		},
		{
			name:     "bare pod falls back to its name",
			pod:      podWith(api.ObjectMeta{Name: "debug-shell"}),
			expected: "debug-shell",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if service := tailer.service(&testCase.pod); service != testCase.expected {
				t.Errorf("expected service %q, got %q", testCase.expected, service)
			}
		})
	}
}

func podWith(meta api.ObjectMeta) pod {
	target := pod{}
	target.Metadata = meta
	return target
}
