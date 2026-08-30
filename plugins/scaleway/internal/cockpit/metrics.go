package cockpit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// Sample is one metric value read from Cockpit, already reduced to the
// container it belongs to.
type Sample struct {
	Metric     string
	Value      float64
	Timestamp  time.Time
	ResourceID string
	Region     string
}

// MetricClient queries the Cockpit Prometheus API.
type MetricClient struct {
	URL   string
	Token string
	HTTP  *http.Client
}

func NewMetricClient(endpoint string, token string, client *http.Client) *MetricClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	return &MetricClient{URL: strings.TrimSuffix(endpoint, "/"), Token: token, HTTP: client}
}

// Query is one metric tezcatl collects: a name to publish it under and
// the PromQL that produces it.
type Query struct {
	Name  string
	Query string
}

// DefaultQueries covers what a serverless container exposes. Every one
// of them aggregates by resource_id on purpose: Cockpit labels samples
// with resource_instance too, and an instance identifier changes at
// every scale-up. Keeping it would mint a new series per instance,
// each written once and never fed again, which inflates the detector's
// state without ever producing a usable baseline.
func DefaultQueries() []Query {
	return []Query{
		{
			Name:  "serverless.cpu.percent",
			Query: `avg by (resource_id, region) (serverless_container_cpu_usage_ratio)`,
		},
		{
			Name: "serverless.memory.used_percent",
			Query: `avg by (resource_id, region) (` +
				`serverless_container_memory_usage_bytes / serverless_container_memory_limit_bytes * 100)`,
		},
		{
			Name:  "serverless.instances",
			Query: `max by (resource_id, region) (serverless_container_instances_total)`,
		},
		{
			Name:  "serverless.requests_per_second",
			Query: `sum by (resource_id, region) (serverless_container_requests_per_second)`,
		},
	}
}

type promResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// Collect evaluates every query and returns one sample per container.
func (c *MetricClient) Collect(ctx context.Context, queries []Query) ([]Sample, error) {
	samples := []Sample{}

	for _, query := range queries {
		results, err := c.evaluate(ctx, query)
		if err != nil {
			// One failing query must not hide the others: a metric may
			// be missing simply because no container ran recently.
			continue
		}

		samples = append(samples, results...)
	}

	return samples, nil
}

func (c *MetricClient) evaluate(ctx context.Context, query Query) ([]Sample, error) {
	values := url.Values{}
	values.Set("query", query.Query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.URL+"/prometheus/api/v1/query?"+values.Encode(), nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	req.Header.Set("X-Token", c.Token)

	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, errors.Errorf("prometheus query returned %s", res.Status)
	}

	var decoded promResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return nil, errors.Wrap(err, "malformed prometheus response")
	}

	if decoded.Status != "success" {
		return nil, errors.Errorf("prometheus query failed: %s", decoded.Error)
	}

	samples := make([]Sample, 0, len(decoded.Data.Result))

	for _, result := range decoded.Data.Result {
		value, ok := parseSample(result.Value)
		if !ok {
			continue
		}

		timestamp, ok := parseTimestamp(result.Value)
		if !ok {
			timestamp = time.Now()
		}

		samples = append(samples, Sample{
			Metric:     query.Name,
			Value:      value,
			Timestamp:  timestamp,
			ResourceID: result.Metric["resource_id"],
			Region:     result.Metric["region"],
		})
	}

	return samples, nil
}

// parseSample reads the value of a Prometheus instant vector, which is
// a string so it survives JSON without losing precision.
func parseSample(value [2]any) (float64, bool) {
	raw, ok := value[1].(string)
	if !ok {
		return 0, false
	}

	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}

	return parsed, true
}

func parseTimestamp(value [2]any) (time.Time, bool) {
	seconds, ok := value[0].(float64)
	if !ok {
		return time.Time{}, false
	}

	return time.Unix(int64(seconds), int64((seconds-float64(int64(seconds)))*1e9)), true
}
