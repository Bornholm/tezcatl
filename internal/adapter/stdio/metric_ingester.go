package stdio

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/format/prometheus"
	"github.com/pkg/errors"
)

// MetricIngester reads newline-delimited samples in the Prometheus text
// exposition format and emits one metric observation per sample.
// Malformed lines are logged and skipped so a partially broken exporter
// does not stop ingestion.
type MetricIngester struct {
	reader   io.Reader
	identity Identity
	now      func() time.Time
}

func NewMetricIngester(reader io.Reader, identity Identity) *MetricIngester {
	return &MetricIngester{
		reader:   reader,
		identity: identity,
		now:      time.Now,
	}
}

func (i *MetricIngester) Ingest(ctx context.Context, out chan<- model.Observation) error {
	scanner := bufio.NewScanner(i.reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		sample, timestamp, err := prometheus.ParseLine(scanner.Text())
		if err != nil {
			slog.WarnContext(ctx, "skipping malformed metric line", slog.String("service", i.identity.Service), slog.Any("error", errors.WithStack(err)))
			continue
		}

		if sample == nil {
			continue
		}

		now := i.now()
		if timestamp.IsZero() {
			timestamp = now
		}

		obs := model.Observation{
			ID:          model.NewID(),
			Service:     i.identity.Service,
			Environment: i.identity.Environment,
			Modality:    model.ModalityMetric,
			Timestamp:   timestamp,
			IngestedAt:  now,
			Metric:      sample,
		}

		select {
		case out <- obs:
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		}
	}

	if err := scanner.Err(); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
