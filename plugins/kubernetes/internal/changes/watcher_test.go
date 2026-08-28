package changes

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

func deployment(name string, generation int, replicas int, image string, annotations string) string {
	return fmt.Sprintf(`{
		"metadata": {
			"name": "%s",
			"namespace": "prod-ns",
			"uid": "uid-%s",
			"generation": %d,
			"labels": {"app": "%s"}
		},
		"spec": {
			"replicas": %d,
			"template": {
				"metadata": {"annotations": {%s}},
				"spec": {"containers": [{"name": "web", "image": "%s"}]}
			}
		}
	}`, name, name, generation, name, replicas, annotations, image)
}

// fakeAPIServer serves one deployment as baseline, then a watch stream
// replaying a full lifecycle: image rollout, status churn, rollout
// restart, scaling, and a workload created after the baseline. The
// statefulsets and daemonsets collections stay empty.
func fakeAPIServer(t *testing.T) *api.Client {
	t.Helper()

	mux := http.NewServeMux()

	empty := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") == "true" {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}

		fmt.Fprint(w, `{"metadata":{"resourceVersion":"1"},"items":[]}`)
	}
	mux.HandleFunc("/apis/apps/v1/statefulsets", empty)
	mux.HandleFunc("/apis/apps/v1/daemonsets", empty)

	mux.HandleFunc("/apis/apps/v1/deployments", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") != "true" {
			fmt.Fprintf(w, `{"metadata":{"resourceVersion":"1"},"items":[%s]}`,
				deployment("checkout", 1, 2, "ghcr.io/acme/checkout:v1.8.1", ""))
			return
		}

		frame := func(kind string, object string) {
			fmt.Fprintf(w, `{"type":%q,"object":%s}`+"\n", kind, object)
		}

		// Image rollout: generation bump + new image.
		frame("MODIFIED", deployment("checkout", 2, 2, "ghcr.io/acme/checkout:v1.8.2", ""))
		// Status churn: same generation, must not emit.
		frame("MODIFIED", deployment("checkout", 2, 2, "ghcr.io/acme/checkout:v1.8.2", ""))
		// kubectl rollout restart.
		frame("MODIFIED", deployment("checkout", 3, 2, "ghcr.io/acme/checkout:v1.8.2",
			`"kubectl.kubernetes.io/restartedAt": "2026-08-28T10:00:00Z"`))
		// Scale up.
		frame("MODIFIED", deployment("checkout", 4, 5, "ghcr.io/acme/checkout:v1.8.2",
			`"kubectl.kubernetes.io/restartedAt": "2026-08-28T10:00:00Z"`))
		// A workload created after the baseline is a deployment.
		frame("ADDED", deployment("api", 1, 1, "ghcr.io/acme/api:v2.0.0", ""))

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

func TestWatcher(t *testing.T) {
	client := fakeAPIServer(t)

	watcher, err := NewWatcher(&Options{Client: client})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan model.Observation, 16)
	done := make(chan struct{})

	go func() {
		defer close(done)
		watcher.Ingest(ctx, out)
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

	rollout := collect()
	restart := collect()
	scale := collect()
	created := collect()

	cancel()
	<-done

	if rollout.Modality != model.ModalityChange || rollout.Service != "checkout" || rollout.Environment != "prod-ns" {
		t.Errorf("unexpected observation: %+v", rollout)
	}

	// Version is the shortened image: base name and tag.
	if rollout.Change.Type != "deployment" || rollout.Change.Version != "checkout:v1.8.2" {
		t.Errorf("unexpected rollout change: %+v", rollout.Change)
	}

	if expected := "Deployment checkout: image web: ghcr.io/acme/checkout:v1.8.1 -> ghcr.io/acme/checkout:v1.8.2"; rollout.Change.Summary != expected {
		t.Errorf("unexpected summary: %q", rollout.Change.Summary)
	}

	if restart.Change.Type != "restart" {
		t.Errorf("expected a restart change, got %+v", restart.Change)
	}

	if scale.Change.Type != "scale" || scale.Change.Summary != "Deployment checkout: replicas 2 -> 5" {
		t.Errorf("unexpected scale change: %+v", scale.Change)
	}

	if created.Change.Type != "deployment" || created.Service != "api" || created.Change.Version != "api:v2.0.0" {
		t.Errorf("unexpected created change: %+v", created.Change)
	}

	if rollout.Attributes[AttrKind] != "Deployment" || rollout.Attributes[AttrNamespace] != "prod-ns" {
		t.Errorf("unexpected attributes: %v", rollout.Attributes)
	}
}
