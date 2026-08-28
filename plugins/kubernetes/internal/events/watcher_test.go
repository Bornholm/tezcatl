package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/plugins/kubernetes/internal/api"
)

// fakeAPIServer serves a minimal events endpoint: an empty list, then a
// watch stream carrying two events and staying open.
func fakeAPIServer(t *testing.T) *api.Client {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") != "true" {
			fmt.Fprint(w, `{"metadata":{"resourceVersion":"100"},"items":[]}`)
			return
		}

		fmt.Fprintln(w, `{"type":"ADDED","object":{
			"metadata":{"namespace":"prod-ns","creationTimestamp":"2026-08-28T09:00:00Z"},
			"type":"Warning","reason":"BackOff","message":"Back-off restarting failed container",
			"lastTimestamp":"2026-08-28T10:00:00Z",
			"involvedObject":{"kind":"Pod","name":"checkout-abc","namespace":"prod-ns"}}}`)
		fmt.Fprintln(w, `{"type":"MODIFIED","object":{
			"metadata":{"namespace":"prod-ns"},
			"type":"Normal","reason":"Pulled","message":"Container image already present",
			"eventTime":"2026-08-28T10:01:00.500000Z",
			"involvedObject":{"kind":"Pod","name":"checkout-abc","namespace":"prod-ns"}}}`)
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

	warning := collect()
	normal := collect()

	cancel()
	<-done

	if warning.Service != DefaultService || warning.Environment != "prod-ns" {
		t.Errorf("unexpected identity: %s/%s", warning.Environment, warning.Service)
	}

	if expected := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC); !warning.Timestamp.Equal(expected) {
		t.Errorf("expected lastTimestamp %s, got %s", expected, warning.Timestamp)
	}

	envelope := map[string]string{}
	if err := json.Unmarshal([]byte(warning.Log.Raw), &envelope); err != nil {
		t.Fatalf("raw line is not a JSON envelope: %+v", err)
	}

	if envelope["level"] != "warn" {
		t.Errorf("expected warn level, got %q", envelope["level"])
	}

	if expected := "Pod/checkout-abc BackOff: Back-off restarting failed container"; envelope["msg"] != expected {
		t.Errorf("expected message %q, got %q", expected, envelope["msg"])
	}

	if warning.Attributes[AttrReason] != "BackOff" || warning.Attributes[AttrKind] != "Pod" {
		t.Errorf("unexpected attributes: %v", warning.Attributes)
	}

	// MODIFIED frames (count increments) are shipped too, with the
	// eventTime fallback and an info level.
	if !strings.Contains(normal.Log.Raw, `"level":"info"`) {
		t.Errorf("expected info level, got %q", normal.Log.Raw)
	}

	if expected := time.Date(2026, 8, 28, 10, 1, 0, 500000000, time.UTC); !normal.Timestamp.Equal(expected) {
		t.Errorf("expected eventTime %s, got %s", expected, normal.Timestamp)
	}
}
