package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func TestEventSinkPostsEvents(t *testing.T) {
	received := make(chan model.Event, 4)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing authorization header")
		}

		var evt model.Event
		if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
			t.Errorf("malformed body: %+v", err)
		}

		received <- evt
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sink, err := NewEventSink(server.URL, map[string]string{"Authorization": "Bearer secret"})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	events := []model.Event{
		{ID: "evt-1", Kind: "anomaly.log.new_template", Source: "prod/api"},
		{ID: "evt-2", Kind: "anomaly.correlated", Source: "prod/api"},
	}

	if err := sink.Publish(context.Background(), events); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	for _, want := range events {
		got := <-received
		if got.ID != want.ID || got.Kind != want.Kind {
			t.Errorf("expected event %+v, got %+v", want, got)
		}
	}
}

func TestEventSinkFailsOnErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	sink, err := NewEventSink(server.URL, nil)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if err := sink.Publish(context.Background(), []model.Event{{ID: "evt-1"}}); err == nil {
		t.Fatal("expected error on non-2xx status")
	}
}
