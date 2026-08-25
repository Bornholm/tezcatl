package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

// Metric names produced by the collector, one series per container.
const (
	MetricCPUPercent        = "docker.cpu.percent"
	MetricMemoryUsedPercent = "docker.memory.used_percent"
	MetricContainersRunning = "docker.containers.running"
)

// DefaultServiceLabel is the container label used to derive the service
// identity; com.dokku.app-name is set by Dokku on every app container.
const DefaultServiceLabel = "com.dokku.app-name"

type Options struct {
	// Socket is the Docker Engine unix socket path.
	Socket      string
	Interval    time.Duration
	Environment string
	// ServiceLabel derives the service from a container label; when the
	// label is absent, the container name up to the first dot is used
	// (dokku containers are named app.web.1).
	ServiceLabel string
}

// Collector polls the Docker Engine API for per-container CPU and
// memory usage plus a per-service running containers count. It talks
// plain HTTP over the unix socket, no Docker client library involved.
type Collector struct {
	opts   *Options
	client *http.Client
	now    func() time.Time

	prevCPU map[string]cpuSample
}

type cpuSample struct {
	usage  uint64
	system uint64
}

func NewCollector(opts *Options) (*Collector, error) {
	if opts.Socket == "" {
		opts.Socket = "/var/run/docker.sock"
	}

	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}

	if opts.ServiceLabel == "" {
		opts.ServiceLabel = DefaultServiceLabel
	}

	socket := opts.Socket

	return &Collector{
		opts: opts,
		client: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network string, address string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		},
		now:     time.Now,
		prevCPU: map[string]cpuSample{},
	}, nil
}

func (c *Collector) Ingest(ctx context.Context, out chan<- model.Observation) error {
	ticker := time.NewTicker(c.opts.Interval)
	defer ticker.Stop()

	for {
		if err := c.poll(ctx, out); err != nil {
			slog.WarnContext(ctx, "docker poll failed", slog.Any("error", err))
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		}
	}
}

type container struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
}

type containerStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

func (c *Collector) poll(ctx context.Context, out chan<- model.Observation) error {
	containers := []container{}
	if err := c.get(ctx, "/containers/json", &containers); err != nil {
		return errors.WithStack(err)
	}

	now := c.now()
	seen := map[string]struct{}{}
	runningPerService := map[string]int{}

	for _, ctr := range containers {
		seen[ctr.ID] = struct{}{}

		name := containerName(ctr)
		service := c.service(ctr, name)
		runningPerService[service]++

		stats := containerStats{}
		if err := c.get(ctx, fmt.Sprintf("/containers/%s/stats?stream=false&one-shot=true", ctr.ID), &stats); err != nil {
			slog.WarnContext(ctx, "could not read container stats", slog.String("container", name), slog.Any("error", err))
			continue
		}

		labels := map[string]string{"container": name}

		if cpu, ok := c.cpuPercent(ctr.ID, stats); ok {
			c.emit(ctx, out, now, service, model.MetricSample{Name: MetricCPUPercent, Value: cpu, Labels: labels})
		}

		if memory, ok := memoryUsedPercent(stats); ok {
			c.emit(ctx, out, now, service, model.MetricSample{Name: MetricMemoryUsedPercent, Value: memory, Labels: labels})
		}
	}

	for service, count := range runningPerService {
		c.emit(ctx, out, now, service, model.MetricSample{Name: MetricContainersRunning, Value: float64(count)})
	}

	// Forget containers that are gone.
	for id := range c.prevCPU {
		if _, exists := seen[id]; !exists {
			delete(c.prevCPU, id)
		}
	}

	return nil
}

func (c *Collector) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return errors.WithStack(err)
	}

	res, err := c.client.Do(req)
	if err != nil {
		return errors.WithStack(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return errors.Errorf("unexpected status %d for %s", res.StatusCode, path)
	}

	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func containerName(ctr container) string {
	if len(ctr.Names) == 0 {
		return ctr.ID[:min(12, len(ctr.ID))]
	}

	return strings.TrimPrefix(ctr.Names[0], "/")
}

func (c *Collector) service(ctr container, name string) string {
	if service, exists := ctr.Labels[c.opts.ServiceLabel]; exists && service != "" {
		return service
	}

	if base, _, found := strings.Cut(name, "."); found && base != "" {
		return base
	}

	return name
}

// cpuPercent computes the container CPU usage between two polls; the
// first poll only records the baseline.
func (c *Collector) cpuPercent(id string, stats containerStats) (float64, bool) {
	sample := cpuSample{
		usage:  stats.CPUStats.CPUUsage.TotalUsage,
		system: stats.CPUStats.SystemCPUUsage,
	}

	prev, exists := c.prevCPU[id]
	c.prevCPU[id] = sample

	if !exists || sample.system <= prev.system || sample.usage < prev.usage {
		return 0, false
	}

	cpus := float64(stats.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = 1
	}

	deltaUsage := float64(sample.usage - prev.usage)
	deltaSystem := float64(sample.system - prev.system)

	return 100 * deltaUsage / deltaSystem * cpus, true
}

// memoryUsedPercent mirrors what docker stats displays: the page cache
// (inactive_file on cgroup v2, cache on v1) is not counted as used.
func memoryUsedPercent(stats containerStats) (float64, bool) {
	usage := stats.MemoryStats.Usage
	limit := stats.MemoryStats.Limit

	if limit == 0 {
		return 0, false
	}

	if inactive, exists := stats.MemoryStats.Stats["inactive_file"]; exists && inactive < usage {
		usage -= inactive
	} else if cache, exists := stats.MemoryStats.Stats["cache"]; exists && cache < usage {
		usage -= cache
	}

	return 100 * float64(usage) / float64(limit), true
}

func (c *Collector) emit(ctx context.Context, out chan<- model.Observation, now time.Time, service string, sample model.MetricSample) {
	obs := model.Observation{
		ID:          model.NewID(),
		Service:     service,
		Environment: c.opts.Environment,
		Modality:    model.ModalityMetric,
		Timestamp:   now,
		IngestedAt:  now,
		Metric:      &sample,
	}

	select {
	case out <- obs:
	case <-ctx.Done():
	}
}
