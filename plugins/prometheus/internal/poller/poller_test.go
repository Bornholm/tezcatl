package poller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

const vectorFixture = `{
	"status": "success",
	"data": {
		"resultType": "vector",
		"result": [
			{"metric": {"service": "checkout", "instance": "10.0.0.1:8080"}, "value": [1787200920.123, "0.245"]},
			{"metric": {"service": "payments", "instance": "10.0.0.2:8080"}, "value": [1787200920.123, "1.532"]}
		]
	}
}`

func TestPollerEmitsObservations(t *testing.T) {
	queries := make(chan string, 16)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries <- r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(vectorFixture))
	}))
	defer server.Close()

	poller, err := NewPoller(&Options{
		URL:         server.URL,
		Interval:    time.Hour, // A single poll: the first one is immediate.
		Service:     "fallback",
		Environment: "prod",
		Queries: []Query{
			{Name: "latency_p95", Query: `histogram_quantile(0.95, http_request_duration_seconds_bucket)`, ServiceLabel: "service"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan model.Observation, 16)

	done := make(chan struct{})
	go func() {
		defer close(done)
		poller.Ingest(ctx, out)
	}()

	collected := []model.Observation{}
	timeout := time.After(5 * time.Second)

	for len(collected) < 2 {
		select {
		case obs := <-out:
			collected = append(collected, obs)
		case <-timeout:
			t.Fatalf("expected 2 observations, got %d", len(collected))
		}
	}

	cancel()
	<-done

	if query := <-queries; query != `histogram_quantile(0.95, http_request_duration_seconds_bucket)` {
		t.Errorf("unexpected query sent: %q", query)
	}

	obs := collected[0]

	if obs.Modality != model.ModalityMetric || obs.Metric.Name != "latency_p95" {
		t.Fatalf("unexpected observation: %+v", obs)
	}

	if obs.Service != "checkout" || obs.Environment != "prod" {
		t.Errorf("expected identity derived from label, got %q/%q", obs.Environment, obs.Service)
	}

	if obs.Metric.Value != 0.245 {
		t.Errorf("unexpected value: %f", obs.Metric.Value)
	}

	if obs.Timestamp.Unix() != 1787200920 {
		t.Errorf("unexpected timestamp: %v", obs.Timestamp)
	}

	if collected[1].Service != "payments" {
		t.Errorf("expected second sample service payments, got %q", collected[1].Service)
	}
}

func TestPollerRejectsInvalidOptions(t *testing.T) {
	if _, err := NewPoller(&Options{URL: "http://localhost:9090"}); err == nil {
		t.Error("expected missing queries to be rejected")
	}

	if _, err := NewPoller(&Options{URL: "http://localhost:9090", Queries: []Query{{Name: "x"}}}); err == nil {
		t.Error("expected query without promql to be rejected")
	}

	if _, err := NewPoller(&Options{Queries: []Query{{Name: "x", Query: "up"}}}); err == nil {
		t.Error("expected missing url to be rejected")
	}
}
