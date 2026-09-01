// Package client wraps the tezcatl AdminService for the itztli UI: it
// fetches events, groups them into incidents the same way the CLI
// does, and relays the marking feedback loop.
package client

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/core/incident"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
	googlegrpc "google.golang.org/grpc"
)

type Options struct {
	Target string
	TLSCA  string
	// Window is how far back the incident list looks.
	Window time.Duration
	// Grouping tunes incident.Group; zero values use its defaults.
	Grouping incident.Options
	// CacheTTL is how long a fetched snapshot is reused. Every page
	// load walks the whole window, so even a short TTL keeps htmx
	// interactions from hammering the server.
	CacheTTL time.Duration
}

type Client struct {
	options Options

	conn  *googlegrpc.ClientConn
	admin tezcatlv1.AdminServiceClient

	mutex    sync.Mutex
	snapshot *Snapshot
}

// Snapshot is one consistent read of the server's event log. It keeps
// the anomalies rather than the incidents: grouping depends on the gap
// the reader asked for, so it is recomputed per request while the
// events, which do not depend on it, are fetched once.
type Snapshot struct {
	FetchedAt time.Time
	// TotalEvents counts every event of the window, whatever its
	// kind; the incidents only aggregate the anomalies.
	TotalEvents int
	// Anomalies are the anomaly events of the window, oldest first.
	Anomalies []model.Event
}

// Query is what a reader asked to see.
type Query struct {
	// Since bounds the incidents kept: an incident with no activity
	// after it is not shown. Zero means the whole window.
	Since time.Time
	// MinSeverity keeps the incidents at least this severe. Empty
	// keeps everything.
	MinSeverity model.Severity
	// Gap overrides the configured grouping gap.
	Gap time.Duration
}

// Template mirrors the AdminService's TemplateInfo.
type Template struct {
	Partition string
	Template  string
	Size      int64
	Marking   string
}

// Metric mirrors the AdminService's MetricInfo.
type Metric struct {
	Key     string
	Samples int64
	Mean    float64
	StdDev  float64
	Recent  float64
	Warmup  bool
	Ignored bool
}

func New(options Options) (*Client, error) {
	conn, err := grpc.Dial(options.Target, options.TLSCA)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return &Client{
		options: options,
		conn:    conn,
		admin:   tezcatlv1.NewAdminServiceClient(conn),
	}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

// Incidents groups and filters the cached events for one query. The
// grouping runs on every call, which costs microseconds and lets a
// reader change the gap without waiting for the network.
func (c *Client) Incidents(ctx context.Context, query Query) ([]incident.Incident, *Snapshot, error) {
	snapshot, err := c.snapshotOf(ctx)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}

	options := c.options.Grouping
	if query.Gap > 0 {
		options.Gap = query.Gap
	}

	incidents := incident.Group(snapshot.Anomalies, options)

	kept := incidents[:0]
	for _, entry := range incidents {
		// An incident is in range when it was still active in it: one
		// that started before the window but is still going matters.
		if !query.Since.IsZero() && entry.End.Before(query.Since) {
			continue
		}

		if severityRank(entry.Severity) < severityRank(query.MinSeverity) {
			continue
		}

		kept = append(kept, entry)
	}

	// Group returns oldest first; the UI leads with the latest.
	sort.SliceStable(kept, func(i, j int) bool {
		return kept[i].Start.After(kept[j].Start)
	})

	return kept, snapshot, nil
}

func severityRank(severity model.Severity) int {
	switch severity {
	case model.SeverityCritical:
		return 3
	case model.SeverityWarning:
		return 2
	case model.SeverityInfo:
		return 1
	default:
		return 0
	}
}

// snapshotOf returns the cached events, refetching them when the
// cached read is older than the TTL.
func (c *Client) snapshotOf(ctx context.Context) (*Snapshot, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.snapshot != nil && time.Since(c.snapshot.FetchedAt) < c.options.CacheTTL {
		return c.snapshot, nil
	}

	snapshot, err := c.fetch(ctx)
	if err != nil {
		// A stale snapshot beats an error page while the server
		// restarts, as long as it is not ancient.
		if c.snapshot != nil && time.Since(c.snapshot.FetchedAt) < time.Minute {
			return c.snapshot, nil
		}

		return nil, errors.WithStack(err)
	}

	c.snapshot = snapshot

	return snapshot, nil
}

// Incident finds one incident by its ID, over the whole window and
// whatever the reader's filters: a link must not break because the
// list is currently narrowed. It groups with the given gap, since the
// incident's identity depends on it.
func (c *Client) Incident(ctx context.Context, id string, gap time.Duration) (incident.Incident, bool, error) {
	incidents, _, err := c.Incidents(ctx, Query{Gap: gap})
	if err != nil {
		return incident.Incident{}, false, errors.WithStack(err)
	}

	for _, entry := range incidents {
		if IncidentID(entry) == id {
			return entry, true, nil
		}
	}

	return incident.Incident{}, false, nil
}

// IncidentID names an incident stably across requests. Incidents are
// grouped on the fly and have no server-side identity, but the trigger
// event does; two snapshots grouping the same events agree on it.
func IncidentID(entry incident.Incident) string {
	return entry.Trigger.ID
}

func (c *Client) fetch(ctx context.Context) (*Snapshot, error) {
	since := time.Now().Add(-c.options.Window)

	res, err := c.admin.ListEvents(ctx, &tezcatlv1.ListEventsRequest{
		Since: since.Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	anomalies := make([]model.Event, 0, len(res.GetEvents()))
	for _, envelope := range res.GetEvents() {
		var event model.Event
		if err := json.Unmarshal([]byte(envelope.GetJson()), &event); err != nil {
			return nil, errors.WithStack(err)
		}

		if strings.HasPrefix(event.Kind, "anomaly.") {
			anomalies = append(anomalies, event)
		}
	}

	return &Snapshot{
		FetchedAt:   time.Now(),
		TotalEvents: len(res.GetEvents()),
		Anomalies:   anomalies,
	}, nil
}

// Templates lists the learned templates, biggest first: the templates
// worth marking are the ones the server sees all day.
func (c *Client) Templates(ctx context.Context) ([]Template, error) {
	res, err := c.admin.ListTemplates(ctx, &tezcatlv1.ListTemplatesRequest{})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	templates := make([]Template, 0, len(res.GetTemplates()))
	for _, info := range res.GetTemplates() {
		templates = append(templates, Template{
			Partition: info.GetPartition(),
			Template:  info.GetTemplate(),
			Size:      info.GetSize(),
			Marking:   info.GetMarking(),
		})
	}

	sort.SliceStable(templates, func(i, j int) bool {
		if templates[i].Size != templates[j].Size {
			return templates[i].Size > templates[j].Size
		}

		return templates[i].Template < templates[j].Template
	})

	return templates, nil
}

// Metrics lists the learned series, the furthest from their baseline
// first, so what deserves a look is on top.
func (c *Client) Metrics(ctx context.Context) ([]Metric, error) {
	res, err := c.admin.ListMetrics(ctx, &tezcatlv1.ListMetricsRequest{})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	metrics := make([]Metric, 0, len(res.GetMetrics()))
	for _, info := range res.GetMetrics() {
		metrics = append(metrics, Metric{
			Key:     info.GetKey(),
			Samples: info.GetSamples(),
			Mean:    info.GetMean(),
			StdDev:  info.GetStdDev(),
			Recent:  info.GetRecent(),
			Warmup:  info.GetWarmup(),
			Ignored: info.GetIgnored(),
		})
	}

	sort.SliceStable(metrics, func(i, j int) bool {
		si, sj := metrics[i].Sigma(), metrics[j].Sigma()
		if abs(si) != abs(sj) {
			return abs(si) > abs(sj)
		}

		return metrics[i].Key < metrics[j].Key
	})

	return metrics, nil
}

// Sigma is how many learned standard deviations away the recent level
// sits; 0 when the series has no spread to measure against.
func (m Metric) Sigma() float64 {
	if m.StdDev <= 0 {
		return 0
	}

	return (m.Recent - m.Mean) / m.StdDev
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}

	return f
}

func (c *Client) MarkTemplate(ctx context.Context, template string, marking string) error {
	_, err := c.admin.MarkTemplate(ctx, &tezcatlv1.MarkTemplateRequest{
		Template: template,
		Marking:  marking,
	})

	return errors.WithStack(err)
}

func (c *Client) MarkMetric(ctx context.Context, pattern string, ignore bool) error {
	_, err := c.admin.MarkMetric(ctx, &tezcatlv1.MarkMetricRequest{
		Pattern: pattern,
		Ignore:  ignore,
	})

	return errors.WithStack(err)
}

// Invalidate drops the cached snapshot, so the next page load sees a
// marking's effect immediately instead of after the TTL.
func (c *Client) Invalidate() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.snapshot = nil
}
