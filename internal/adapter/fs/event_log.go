package fs

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

// EventLog persists published events as JSON Lines, one file per day,
// so the server remembers what it emitted across restarts. It is both
// an event sink and the query backend of the admin API. The format is
// deliberately the same JSONL the stdout sink emits: a segment can be
// read with any text tool, no schema to migrate.
type EventLog struct {
	dir       string
	retention time.Duration
	now       func() time.Time

	mu      sync.Mutex
	file    *os.File
	segment string
}

const eventSegmentLayout = "2006-01-02"

func NewEventLog(dir string, retention time.Duration) (*EventLog, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.WithStack(err)
	}

	log := &EventLog{
		dir:       dir,
		retention: retention,
		now:       time.Now,
	}

	log.prune()

	return log, nil
}

// Publish implements port.EventSink.
func (l *EventLog) Publish(ctx context.Context, events []model.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return errors.WithStack(err)
		}

		file, err := l.segmentFile()
		if err != nil {
			return errors.WithStack(err)
		}

		if _, err := file.Write(append(encoded, '\n')); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

// segmentFile returns the current day's file, rotating (and pruning old
// segments) when the day changed. The caller holds the lock.
func (l *EventLog) segmentFile() (*os.File, error) {
	segment := l.now().Format(eventSegmentLayout)

	if l.file != nil && l.segment == segment {
		return l.file, nil
	}

	if l.file != nil {
		l.file.Close()
		l.file = nil
	}

	file, err := os.OpenFile(l.segmentPath(segment), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	l.file = file
	l.segment = segment

	l.prune()

	return file, nil
}

func (l *EventLog) segmentPath(segment string) string {
	return filepath.Join(l.dir, "events-"+segment+".jsonl")
}

// prune deletes segments entirely older than the retention window; a
// zero retention keeps everything.
func (l *EventLog) prune() {
	if l.retention <= 0 {
		return
	}

	// A segment named by day D holds events written until the end of
	// D, so it expires when D+1 is out of the window.
	cutoff := l.now().Add(-l.retention).AddDate(0, 0, -1)

	for _, segment := range l.segments() {
		day, err := time.ParseInLocation(eventSegmentLayout, segment, time.Local)
		if err != nil {
			continue
		}

		if day.Before(cutoff) {
			os.Remove(l.segmentPath(segment))
		}
	}
}

// segments lists the segment days present on disk, oldest first.
func (l *EventLog) segments() []string {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil
	}

	segments := make([]string, 0, len(entries))

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "events-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		segments = append(segments, strings.TrimSuffix(strings.TrimPrefix(name, "events-"), ".jsonl"))
	}

	sort.Strings(segments)

	return segments
}

// Query returns the events whose timestamp falls in [since, until],
// oldest first; zero bounds are unbounded. When limit is positive only
// the newest limit events are kept.
func (l *EventLog) Query(since time.Time, until time.Time, limit int) ([]model.Event, error) {
	events := []model.Event{}

	for _, segment := range l.segments() {
		day, err := time.ParseInLocation(eventSegmentLayout, segment, time.Local)
		if err != nil {
			continue
		}

		// Segment days bound the write time, and event timestamps track
		// it closely; one day of slack on each side covers clock skew
		// and events ingested with a slightly older timestamp.
		if !since.IsZero() && day.AddDate(0, 0, 2).Before(since) {
			continue
		}

		if !until.IsZero() && day.AddDate(0, 0, -1).After(until) {
			continue
		}

		if err := l.scanSegment(segment, since, until, &events); err != nil {
			return nil, errors.WithStack(err)
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}

	return events, nil
}

func (l *EventLog) scanSegment(segment string, since time.Time, until time.Time, events *[]model.Event) error {
	file, err := os.Open(l.segmentPath(segment))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return errors.WithStack(err)
	}

	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		var event model.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			// A torn last line from a concurrent append, or a corrupt
			// one: skip it rather than fail the whole query.
			continue
		}

		if !since.IsZero() && event.Timestamp.Before(since) {
			continue
		}

		if !until.IsZero() && event.Timestamp.After(until) {
			continue
		}

		*events = append(*events, event)
	}

	// A scanner error past the readable prefix (torn write) leaves the
	// events read so far valid.
	return nil
}

func (l *EventLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		err := l.file.Close()
		l.file = nil

		return errors.WithStack(err)
	}

	return nil
}
