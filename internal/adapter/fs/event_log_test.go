package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func logEvent(id string, timestamp time.Time) model.Event {
	return model.Event{ID: id, Kind: "anomaly.log", Source: "prod/api", Timestamp: timestamp}
}

func TestEventLogPublishAndQuery(t *testing.T) {
	log, err := NewEventLog(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	defer log.Close()

	base := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)

	for i := range 5 {
		if err := log.Publish(context.Background(), []model.Event{logEvent(fmt.Sprintf("e%d", i), base.Add(time.Duration(i)*time.Minute))}); err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}
	}

	events, err := log.Query(time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(events) != 5 || events[0].ID != "e0" || events[4].ID != "e4" {
		t.Fatalf("expected e0..e4 in order, got %+v", events)
	}

	// Time bounds are inclusive filters on the event timestamp.
	bounded, err := log.Query(base.Add(1*time.Minute), base.Add(3*time.Minute), 0)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(bounded) != 3 || bounded[0].ID != "e1" || bounded[2].ID != "e3" {
		t.Errorf("expected e1..e3, got %+v", bounded)
	}

	// A positive limit keeps the newest events.
	newest, err := log.Query(time.Time{}, time.Time{}, 2)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(newest) != 2 || newest[0].ID != "e3" || newest[1].ID != "e4" {
		t.Errorf("expected the 2 newest, got %+v", newest)
	}
}

func TestEventLogSurvivesReopen(t *testing.T) {
	dir := t.TempDir()

	log, err := NewEventLog(dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if err := log.Publish(context.Background(), []model.Event{logEvent("e1", time.Now())}); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	log.Close()

	reopened, err := NewEventLog(dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	defer reopened.Close()

	if err := reopened.Publish(context.Background(), []model.Event{logEvent("e2", time.Now())}); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	events, err := reopened.Query(time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected both events across a restart, got %+v", events)
	}
}

func TestEventLogRotationAndRetention(t *testing.T) {
	dir := t.TempDir()

	log, err := NewEventLog(dir, 48*time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	defer log.Close()

	// Drive the clock by hand: one event per day over five days.
	day := time.Date(2026, 8, 20, 12, 0, 0, 0, time.Local)
	for i := range 5 {
		current := day.AddDate(0, 0, i)
		log.now = func() time.Time { return current }

		if err := log.Publish(context.Background(), []model.Event{logEvent(fmt.Sprintf("d%d", i), current)}); err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}
	}

	segments := log.segments()
	if len(segments) >= 5 {
		t.Errorf("expected old segments to be pruned, got %v", segments)
	}

	events, err := log.Query(time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	for _, event := range events {
		if event.ID == "d0" {
			t.Errorf("expected the oldest day to be gone, got %+v", events)
		}
	}

	if len(events) == 0 {
		t.Error("expected recent events to survive retention")
	}
}

func TestEventLogSkipsTornLine(t *testing.T) {
	dir := t.TempDir()

	log, err := NewEventLog(dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	defer log.Close()

	if err := log.Publish(context.Background(), []model.Event{logEvent("e1", time.Now())}); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	// Simulate a torn concurrent append at the end of the segment.
	segment := log.segmentPath(log.now().Format(eventSegmentLayout))
	file, err := os.OpenFile(segment, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if _, err := file.WriteString(`{"id":"torn`); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	file.Close()

	events, err := log.Query(time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(events) != 1 || events[0].ID != "e1" {
		t.Errorf("expected the torn line to be skipped, got %+v", events)
	}

	// The file layout is plain JSONL, one file per day.
	if _, err := os.Stat(filepath.Join(dir, filepath.Base(segment))); err != nil {
		t.Errorf("expected a plain jsonl segment: %+v", err)
	}
}
