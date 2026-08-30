package cockpit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// lokiServer replays canned pages, checking the token and recording the
// query parameters each poll sent.
type lokiServer struct {
	pages   [][]byte
	calls   int
	queries []map[string]string
}

func (s *lokiServer) handler(t *testing.T) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Token"); got != "secret" {
			t.Errorf("expected the cockpit token to be sent, got %q", got)
		}

		params := map[string]string{}
		for key := range r.URL.Query() {
			params[key] = r.URL.Query().Get(key)
		}

		s.queries = append(s.queries, params)

		page := []byte(`{"status":"success","data":{"result":[]}}`)
		if s.calls < len(s.pages) {
			page = s.pages[s.calls]
		}

		s.calls++

		w.Header().Set("Content-Type", "application/json")
		w.Write(page)
	}
}

func page(entries ...[2]string) []byte {
	values := make([][2]string, 0, len(entries))
	values = append(values, entries...)

	body := map[string]any{
		"status": "success",
		"data": map[string]any{
			"result": []map[string]any{
				{
					"stream": map[string]string{
						"resource_id":    "051f3161",
						"resource_name":  "psevetdev-pse-vet-server",
						"resource_type":  "serverless_container",
						"region":         "fr-par",
						"detected_level": "error",
					},
					"values": values,
				},
			},
		},
	}

	encoded, _ := json.Marshal(body)

	return encoded
}

func line(nanos int64, message string) [2]string {
	// Scaleway always sends the three fields together.
	envelope, _ := json.Marshal(map[string]string{
		"resource_instance": "pse-vet-server-00013-deployment-abc",
		"message":           message,
		"stream":            "stdout",
	})

	return [2]string{strconv.FormatInt(nanos, 10), string(envelope)}
}

func TestLogClientUnwrapsEnvelope(t *testing.T) {
	server := &lokiServer{pages: [][]byte{page(line(1788066980517506227, "database connection refused"))}}
	httpServer := httptest.NewServer(server.handler(t))

	defer httpServer.Close()

	client := NewLogClient(httpServer.URL, "secret", httpServer.Client())
	client.Since(time.Unix(0, 1788066980000000000))

	entries, err := client.Poll(context.Background(), `{resource_type="serverless_container"}`, 0, time.Unix(0, 1788066990000000000))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	entry := entries[0]

	if entry.Message != "database connection refused" {
		t.Errorf("expected the envelope to be unwrapped, got %q", entry.Message)
	}

	if entry.Level != "error" {
		t.Errorf("expected the detected level, got %q", entry.Level)
	}

	if entry.ResourceID != "051f3161" || entry.Region != "fr-par" {
		t.Errorf("expected the stream labels to be kept, got %+v", entry)
	}

	if entry.Instance != "pse-vet-server-00013-deployment-abc" {
		t.Errorf("expected the instance as an attribute, got %q", entry.Instance)
	}

	// The raw line survives for the server's own record.
	if entry.Raw == "" {
		t.Error("expected the raw line to be preserved")
	}
}

// TestLogClientDoesNotRepeatEntries is the property that matters with a
// polled API: Loki bounds are inclusive, so the entry sitting exactly
// on the cursor comes back at every poll unless it is remembered.
func TestLogClientDoesNotRepeatEntries(t *testing.T) {
	const at = int64(1788066980517506227)

	server := &lokiServer{pages: [][]byte{
		page(line(at, "first")),
		// The API returns the boundary entry again, plus a new one.
		page(line(at, "first"), line(at+1000, "second")),
	}}

	httpServer := httptest.NewServer(server.handler(t))
	defer httpServer.Close()

	client := NewLogClient(httpServer.URL, "secret", httpServer.Client())
	client.Since(time.Unix(0, at-1000))

	first, err := client.Poll(context.Background(), "{}", 0, time.Unix(0, at+1))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	second, err := client.Poll(context.Background(), "{}", 0, time.Unix(0, at+2000))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(first) != 1 || first[0].Message != "first" {
		t.Fatalf("expected the first entry once, got %+v", first)
	}

	if len(second) != 1 || second[0].Message != "second" {
		t.Fatalf("expected only the new entry on the second poll, got %d: %+v", len(second), second)
	}

	// The second poll must start where the first stopped, not from the
	// original lookback.
	if got := server.queries[1]["start"]; got != strconv.FormatInt(at, 10) {
		t.Errorf("expected the cursor to advance to %d, got %s", at, got)
	}

	// Oldest first, so a truncated page still advances the cursor.
	if got := server.queries[0]["direction"]; got != "forward" {
		t.Errorf("expected a forward query, got %q", got)
	}
}

func TestLogClientKeepsPlainLines(t *testing.T) {
	server := &lokiServer{pages: [][]byte{page([2]string{"1788066980000000000", "plain text line"})}}
	httpServer := httptest.NewServer(server.handler(t))

	defer httpServer.Close()

	client := NewLogClient(httpServer.URL, "secret", httpServer.Client())
	client.Since(time.Unix(0, 1788066979000000000))

	entries, err := client.Poll(context.Background(), "{}", 0, time.Unix(0, 1788066981000000000))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	// Not an envelope: the message is left empty so the server applies
	// its own parsing to the raw line.
	if entries[0].Message != "" || entries[0].Raw != "plain text line" {
		t.Errorf("expected a plain line to be passed through, got %+v", entries[0])
	}
}

func TestLogClientReportsHTTPErrors(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))

	defer httpServer.Close()

	client := NewLogClient(httpServer.URL, "secret", httpServer.Client())

	if _, err := client.Poll(context.Background(), "{}", 0, time.Now()); err == nil {
		t.Fatal("expected a forbidden response to be reported")
	} else if got := fmt.Sprint(err); got == "" {
		t.Error("expected a described error")
	}
}

// TestLogClientDropsBlankLines covers a real trap: Scaleway wraps blank
// container output in an envelope that still carries the instance
// identifier. Mining that envelope would mint a fresh template at every
// deployment, since the identifier changes each time.
func TestLogClientDropsBlankLines(t *testing.T) {
	blank := [2]string{"1788066980000000000", `{"resource_instance":"api-00013-deployment-abc","message":"","stream":"stdout"}`}
	real := line(1788066980000000001, "something happened")

	server := &lokiServer{pages: [][]byte{page(blank, real)}}
	httpServer := httptest.NewServer(server.handler(t))

	defer httpServer.Close()

	client := NewLogClient(httpServer.URL, "secret", httpServer.Client())
	client.Since(time.Unix(0, 1788066979000000000))

	entries, err := client.Poll(context.Background(), "{}", 0, time.Unix(0, 1788066981000000000))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected the blank line to be dropped, got %d entries", len(entries))
	}

	if entries[0].Message != "something happened" {
		t.Errorf("expected the real line to survive, got %q", entries[0].Message)
	}

	if entries[0].Stream != "stdout" {
		t.Errorf("expected the stream to be kept as an attribute, got %q", entries[0].Stream)
	}
}

// TestLogClientLeavesApplicationJSON alone: a container that logs JSON
// itself must reach the server intact, for its own parsing.
func TestLogClientLeavesApplicationJSON(t *testing.T) {
	own := [2]string{"1788066980000000000", `{"level":"error","msg":"pool exhausted"}`}

	server := &lokiServer{pages: [][]byte{page(own)}}
	httpServer := httptest.NewServer(server.handler(t))

	defer httpServer.Close()

	client := NewLogClient(httpServer.URL, "secret", httpServer.Client())
	client.Since(time.Unix(0, 1788066979000000000))

	entries, err := client.Poll(context.Background(), "{}", 0, time.Unix(0, 1788066981000000000))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected the line to be kept, got %d", len(entries))
	}

	// Not Scaleway's wrapper: left whole so the server unwraps it.
	if entries[0].Message != "" || entries[0].Raw != own[1] {
		t.Errorf("expected the application's own json to pass through, got %+v", entries[0])
	}
}
