package system

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

// Metric names produced by the collector.
const (
	MetricCPUPercent        = "system.cpu.percent"
	MetricMemoryUsedPercent = "system.memory.used_percent"
	MetricLoad1             = "system.load1"
	MetricDiskUsedPercent   = "system.disk.used_percent"
)

type Options struct {
	Interval    time.Duration
	Service     string
	Environment string
	// DiskPaths are the mount points whose usage is reported.
	DiskPaths []string

	// procfs overrides /proc in tests.
	procfs string
}

// Collector periodically samples the local machine through /proc and
// statfs: CPU usage, memory usage, 1-minute load average and disk usage
// per mount point. No cgo, Linux only.
type Collector struct {
	opts *Options
	now  func() time.Time

	prevCPU *cpuSample
}

type cpuSample struct {
	busy  uint64
	total uint64
}

func NewCollector(opts *Options) (*Collector, error) {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}

	if opts.Service == "" {
		opts.Service = "host"
	}

	if len(opts.DiskPaths) == 0 {
		opts.DiskPaths = []string{"/"}
	}

	if opts.procfs == "" {
		opts.procfs = "/proc"
	}

	if _, err := os.Stat(opts.procfs); err != nil {
		return nil, errors.Wrapf(err, "procfs %q is not readable", opts.procfs)
	}

	return &Collector{
		opts: opts,
		now:  time.Now,
	}, nil
}

func (c *Collector) Ingest(ctx context.Context, out chan<- model.Observation) error {
	ticker := time.NewTicker(c.opts.Interval)
	defer ticker.Stop()

	for {
		c.poll(ctx, out)

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		}
	}
}

func (c *Collector) poll(ctx context.Context, out chan<- model.Observation) {
	samples := []model.MetricSample{}

	if cpu, ok := c.cpuPercent(ctx); ok {
		samples = append(samples, model.MetricSample{Name: MetricCPUPercent, Value: cpu})
	}

	if memory, err := c.memoryUsedPercent(); err == nil {
		samples = append(samples, model.MetricSample{Name: MetricMemoryUsedPercent, Value: memory})
	} else {
		slog.WarnContext(ctx, "could not sample memory", slog.Any("error", err))
	}

	if load, err := c.load1(); err == nil {
		samples = append(samples, model.MetricSample{Name: MetricLoad1, Value: load})
	} else {
		slog.WarnContext(ctx, "could not sample load average", slog.Any("error", err))
	}

	for _, path := range c.opts.DiskPaths {
		used, err := diskUsedPercent(path)
		if err != nil {
			slog.WarnContext(ctx, "could not sample disk usage", slog.String("path", path), slog.Any("error", err))
			continue
		}

		samples = append(samples, model.MetricSample{
			Name:   MetricDiskUsedPercent,
			Value:  used,
			Labels: map[string]string{"path": path},
		})
	}

	now := c.now()

	for _, sample := range samples {
		obs := model.Observation{
			ID:          model.NewID(),
			Service:     c.opts.Service,
			Environment: c.opts.Environment,
			Modality:    model.ModalityMetric,
			Timestamp:   now,
			IngestedAt:  now,
			Metric:      &sample,
		}

		select {
		case out <- obs:
		case <-ctx.Done():
			return
		}
	}
}

// cpuPercent computes the CPU busy ratio between two polls; the first
// poll only records the baseline.
func (c *Collector) cpuPercent(ctx context.Context) (float64, bool) {
	sample, err := c.readCPU()
	if err != nil {
		slog.WarnContext(ctx, "could not sample cpu", slog.Any("error", err))
		return 0, false
	}

	prev := c.prevCPU
	c.prevCPU = sample

	if prev == nil || sample.total <= prev.total {
		return 0, false
	}

	deltaBusy := float64(sample.busy - prev.busy)
	deltaTotal := float64(sample.total - prev.total)

	return 100 * deltaBusy / deltaTotal, true
}

func (c *Collector) readCPU() (*cpuSample, error) {
	data, err := os.ReadFile(filepath.Join(c.opts.procfs, "stat"))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}

		var values []uint64
		for _, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return nil, errors.Wrapf(err, "malformed cpu line %q", line)
			}

			values = append(values, value)
		}

		sample := &cpuSample{}
		for i, value := range values {
			sample.total += value

			// Fields: user nice system idle iowait irq softirq steal…
			// idle (3) and iowait (4) are the non-busy states.
			if i != 3 && i != 4 {
				sample.busy += value
			}
		}

		return sample, nil
	}

	return nil, errors.New("no aggregate cpu line in /proc/stat")
}

func (c *Collector) memoryUsedPercent() (float64, error) {
	data, err := os.ReadFile(filepath.Join(c.opts.procfs, "meminfo"))
	if err != nil {
		return 0, errors.WithStack(err)
	}

	var total, available float64

	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}

		switch fields[0] {
		case "MemTotal:":
			total = value
		case "MemAvailable:":
			available = value
		}
	}

	if total == 0 {
		return 0, errors.New("missing MemTotal in /proc/meminfo")
	}

	return 100 * (1 - available/total), nil
}

func (c *Collector) load1() (float64, error) {
	data, err := os.ReadFile(filepath.Join(c.opts.procfs, "loadavg"))
	if err != nil {
		return 0, errors.WithStack(err)
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, errors.New("empty /proc/loadavg")
	}

	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, errors.WithStack(err)
	}

	return load, nil
}

