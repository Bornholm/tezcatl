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
	reader io.Reader
	source string
	now    func() time.Time
}

func NewMetricIngester(reader io.Reader, source string) *MetricIngester {
	return &MetricIngester{
		reader: reader,
		source: source,
		now:    time.Now,
	}
}

func (i *MetricIngester) Ingest(ctx context.Context, out chan<- model.Observation) error {
	scanner := bufio.NewScanner(i.reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		sample, timestamp, err := prometheus.ParseLine(scanner.Text())
		if err != nil {
			slog.WarnContext(ctx, "skipping malformed metric line", slog.String("source", i.source), slog.Any("error", errors.WithStack(err)))
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
			ID:         model.NewID(),
			Source:     i.source,
			Modality:   model.ModalityMetric,
			Timestamp:  timestamp,
			IngestedAt: now,
			Metric:     sample,
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
