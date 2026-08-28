package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestListWatch exercises the loop across a watch expiry: a first watch
// delivers one event then ends cleanly (server-side timeout), the loop
// re-lists and a second watch delivers the next event. Bookmarks are
// skipped and the bearer token is sent on every request.
func TestListWatch(t *testing.T) {
	watches := atomic.Int32{}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/things", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing bearer token, got %q", r.Header.Get("Authorization"))
		}

		if r.URL.Query().Get("watch") != "true" {
			fmt.Fprint(w, `{"metadata":{"resourceVersion":"1"},"items":[]}`)
			return
		}

		switch watches.Add(1) {
		case 1:
			fmt.Fprintln(w, `{"type":"BOOKMARK","object":{"metadata":{"resourceVersion":"2"}}}`)
			fmt.Fprintln(w, `{"type":"ADDED","object":{"value":"first"}}`)
			// Handler returns: clean EOF, as on a watch timeout.
		case 2:
			fmt.Fprintln(w, `{"type":"ADDED","object":{"value":"second"}}`)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := NewClient(&Config{Server: server.URL, Token: "secret"})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lists := atomic.Int32{}
	received := make(chan string, 8)
	done := make(chan struct{})

	go func() {
		defer close(done)
		client.ListWatch(ctx, "/api/v1/things", nil,
			func(json.RawMessage) error {
				lists.Add(1)
				return nil
			},
			func(event *WatchEvent) error {
				received <- string(event.Object)
				return nil
			})
	}()

	collect := func() string {
		select {
		case object := <-received:
			return object
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a watch event")
			return ""
		}
	}

	first := collect()
	second := collect()

	cancel()
	<-done

	if first != `{"value":"first"}` || second != `{"value":"second"}` {
		t.Errorf("unexpected events: %q, %q", first, second)
	}

	if lists.Load() != 2 {
		t.Errorf("expected a re-list after the watch expiry, got %d lists", lists.Load())
	}
}
