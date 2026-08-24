package stdio

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

// ChangeIngester reads change records as JSON Lines, one object per
// line:
//
//	{"time": "2026-08-24T14:02:00Z", "type": "deployment", "version": "v1.8.2", "summary": "…"}
//
// time is optional (defaults to the ingestion time). Malformed lines are
// logged and skipped.
type ChangeIngester struct {
	reader   io.Reader
	identity Identity
	now      func() time.Time
}

type changeLine struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Version string    `json:"version"`
	Summary string    `json:"summary"`
}

func NewChangeIngester(reader io.Reader, identity Identity) *ChangeIngester {
	return &ChangeIngester{
		reader:   reader,
		identity: identity,
		now:      time.Now,
	}
}

func (i *ChangeIngester) Ingest(ctx context.Context, out chan<- model.Observation) error {
	scanner := bufio.NewScanner(i.reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var change changeLine
		if err := json.Unmarshal([]byte(line), &change); err != nil {
			slog.WarnContext(ctx, "skipping malformed change line", slog.String("service", i.identity.Service), slog.Any("error", errors.WithStack(err)))
			continue
		}

		now := i.now()

		obs := model.Observation{
			ID:          model.NewID(),
			Service:     i.identity.Service,
			Environment: i.identity.Environment,
			Modality:    model.ModalityChange,
			Timestamp:   change.Time,
			IngestedAt:  now,
			Change: &model.ChangeRecord{
				Type:    change.Type,
				Version: change.Version,
				Summary: change.Summary,
			},
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
