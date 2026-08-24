package postgres

import (
	"context"
	"encoding/json"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pkg/errors"
)

const schema = `
CREATE TABLE IF NOT EXISTS tezcatl_events (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	source TEXT NOT NULL,
	timestamp TIMESTAMPTZ NOT NULL,
	severity TEXT NOT NULL,
	confidence DOUBLE PRECISION NOT NULL,
	summary TEXT NOT NULL,
	payload JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tezcatl_events_source_timestamp_idx
	ON tezcatl_events (source, timestamp DESC);
`

// EventSink stores events in a PostgreSQL table, the full event being
// kept as JSONB alongside the indexed columns.
type EventSink struct {
	pool *pgxpool.Pool
}

// NewEventSink connects to PostgreSQL (dsn follows the libpq/pgx
// conventions, e.g. postgres://user:pass@host:5432/db) and ensures the
// events table exists.
func NewEventSink(ctx context.Context, dsn string) (*EventSink, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, errors.Wrap(err, "could not create events table")
	}

	return &EventSink{pool: pool}, nil
}

func (s *EventSink) Publish(ctx context.Context, events []model.Event) error {
	if len(events) == 0 {
		return nil
	}

	batch := &pgx.Batch{}

	for _, evt := range events {
		payload, err := json.Marshal(evt)
		if err != nil {
			return errors.WithStack(err)
		}

		batch.Queue(`
			INSERT INTO tezcatl_events (id, kind, source, timestamp, severity, confidence, summary, payload)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (id) DO NOTHING`,
			evt.ID, evt.Kind, evt.Source, evt.Timestamp, string(evt.Severity), evt.Confidence, evt.Summary, payload,
		)
	}

	if err := s.pool.SendBatch(ctx, batch).Close(); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func (s *EventSink) Close() error {
	s.pool.Close()

	return nil
}
