package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

const requestTimeout = 15 * time.Second

// Query is one PromQL query evaluated at every polling interval. Name
// becomes the logical metric name of the produced observations.
type Query struct {
	Name  string `yaml:"name"`
	Query string `yaml:"query"`
	// Service/Environment override the poller defaults for this query.
	Service     string `yaml:"service"`
	Environment string `yaml:"environment"`
	// ServiceLabel derives the service from a label of each result
	// sample (e.g. "service" or "job"), taking precedence over Service.
	ServiceLabel string `yaml:"service_label"`
}

type Options struct {
	// URL is the Prometheus base URL, e.g. http://localhost:9090.
	URL      string
	Interval time.Duration
	// Service/Environment are the default identity of polled metrics.
	Service     string
	Environment string
	Queries     []Query
}

// Poller periodically evaluates PromQL instant queries against the
// Prometheus HTTP API and emits one metric observation per result
// sample. Failed polls are logged and retried at the next interval.
type Poller struct {
	opts   *Options
	client *http.Client
}

func NewPoller(opts *Options) (*Poller, error) {
	if opts.URL == "" {
		return nil, errors.New("missing prometheus url")
	}

	if len(opts.Queries) == 0 {
		return nil, errors.New("no prometheus query configured")
	}

	for _, query := range opts.Queries {
		if query.Name == "" || query.Query == "" {
			return nil, errors.Errorf("prometheus queries require both name and query, got %+v", query)
		}
	}

	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}

	return &Poller{
		opts:   opts,
		client: &http.Client{Timeout: requestTimeout},
	}, nil
}

func (p *Poller) Ingest(ctx context.Context, out chan<- model.Observation) error {
	ticker := time.NewTicker(p.opts.Interval)
	defer ticker.Stop()

	for {
		p.poll(ctx, out)

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		}
	}
}

func (p *Poller) poll(ctx context.Context, out chan<- model.Observation) {
	for _, query := range p.opts.Queries {
		samples, err := p.evaluate(ctx, query.Query)
		if err != nil {
			slog.WarnContext(ctx, "prometheus query failed", slog.String("query", query.Name), slog.Any("error", err))
			continue
		}

		for _, sample := range samples {
			obs := p.observation(query, sample)

			select {
			case out <- obs:
			case <-ctx.Done():
				return
			}
		}
	}
}

type apiResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     json.RawMessage
	} `json:"data"`
}

type vectorSample struct {
	Metric map[string]string `json:"metric"`
	Value  [2]any            `json:"value"`
}

type sample struct {
	labels    map[string]string
	value     float64
	timestamp time.Time
}

func (p *Poller) evaluate(ctx context.Context, promql string) ([]sample, error) {
	endpoint := fmt.Sprintf("%s/api/v1/query?query=%s", p.opts.URL, url.QueryEscape(promql))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	res, err := p.client.Do(req)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, errors.Errorf("unexpected status %d", res.StatusCode)
	}

	var decoded apiResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return nil, errors.WithStack(err)
	}

	if decoded.Status != "success" {
		return nil, errors.Errorf("query failed: %s", decoded.Error)
	}

	switch decoded.Data.ResultType {
	case "vector":
		var vector []vectorSample
		if err := json.Unmarshal(decoded.Data.Result, &vector); err != nil {
			return nil, errors.WithStack(err)
		}

		samples := make([]sample, 0, len(vector))
		for _, v := range vector {
			s, err := parseSample(v.Metric, v.Value)
			if err != nil {
				return nil, errors.WithStack(err)
			}

			samples = append(samples, s)
		}

		return samples, nil

	case "scalar":
		var value [2]any
		if err := json.Unmarshal(decoded.Data.Result, &value); err != nil {
			return nil, errors.WithStack(err)
		}

		s, err := parseSample(nil, value)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		return []sample{s}, nil

	default:
		return nil, errors.Errorf("unsupported result type %q", decoded.Data.ResultType)
	}
}

func parseSample(labels map[string]string, value [2]any) (sample, error) {
	seconds, ok := value[0].(float64)
	if !ok {
		return sample{}, errors.Errorf("malformed sample timestamp %v", value[0])
	}

	raw, ok := value[1].(string)
	if !ok {
		return sample{}, errors.Errorf("malformed sample value %v", value[1])
	}

	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return sample{}, errors.Wrapf(err, "malformed sample value %q", raw)
	}

	return sample{
		labels:    labels,
		value:     parsed,
		timestamp: time.Unix(int64(seconds), int64((seconds-float64(int64(seconds)))*1e9)),
	}, nil
}

func (p *Poller) observation(query Query, s sample) model.Observation {
	service := p.opts.Service
	if query.Service != "" {
		service = query.Service
	}

	if query.ServiceLabel != "" {
		if fromLabel, exists := s.labels[query.ServiceLabel]; exists && fromLabel != "" {
			service = fromLabel
		}
	}

	environment := p.opts.Environment
	if query.Environment != "" {
		environment = query.Environment
	}

	return model.Observation{
		ID:          model.NewID(),
		Service:     service,
		Environment: environment,
		Modality:    model.ModalityMetric,
		Timestamp:   s.timestamp,
		IngestedAt:  time.Now(),
		Metric: &model.MetricSample{
			Name:   query.Name,
			Value:  s.value,
			Labels: s.labels,
		},
	}
}
