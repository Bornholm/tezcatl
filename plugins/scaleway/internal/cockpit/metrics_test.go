package cockpit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricClientCollect(t *testing.T) {
	var seen []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Token"); got != "secret" {
			t.Errorf("expected the cockpit token, got %q", got)
		}

		query := r.URL.Query().Get("query")
		seen = append(seen, query)

		if strings.Contains(query, "instances_total") {
			// A metric with no data: no container ran recently.
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))

			return
		}

		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"resource_id":"051f3161","region":"fr-par"},"value":[1788066980.5,"5.898856034373359"]}
		]}}`))
	}))

	defer server.Close()

	client := NewMetricClient(server.URL, "secret", server.Client())

	samples, err := client.Collect(context.Background(), DefaultQueries())
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(samples) == 0 {
		t.Fatal("expected samples")
	}

	first := samples[0]
	if first.ResourceID != "051f3161" || first.Region != "fr-par" {
		t.Errorf("expected the container identity, got %+v", first)
	}

	if first.Value < 5.89 || first.Value > 5.9 {
		t.Errorf("expected the sample value, got %v", first.Value)
	}

	if first.Timestamp.Unix() != 1788066980 {
		t.Errorf("expected the sample timestamp, got %s", first.Timestamp)
	}

	// Every default query aggregates by resource_id and drops
	// resource_instance: an instance identifier changes at every
	// scale-up, and keeping it would mint a series per instance that is
	// written once and never fed again.
	for _, query := range seen {
		if strings.Contains(query, "resource_instance") {
			t.Errorf("expected no per-instance grouping, got %q", query)
		}

		if !strings.Contains(query, "by (resource_id") {
			t.Errorf("expected an aggregation by resource_id, got %q", query)
		}
	}
}

// TestMetricClientSurvivesOneFailingQuery keeps a missing metric from
// hiding the others: a container that has never run has no series.
func TestMetricClientSurvivesOneFailingQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), "cpu_usage_ratio") {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"resource_id":"abc"},"value":[1788066980,"1"]}
		]}}`))
	}))

	defer server.Close()

	client := NewMetricClient(server.URL, "secret", server.Client())

	samples, err := client.Collect(context.Background(), DefaultQueries())
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(samples) == 0 {
		t.Error("expected the remaining queries to still produce samples")
	}
}
