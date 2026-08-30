package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCursorRoundTrip covers what makes a restart lossless: the reader
// records where it got to, and the next run resumes there instead of
// replaying what it already ingested.
func TestCursorRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "cursor")

	writer := newCursorWriter(path)
	writer.record("s=1f28;i=6654a2")
	writer.flush()

	if got := readCursor(path); got != "s=1f28;i=6654a2" {
		t.Errorf("expected the cursor to be readable back, got %q", got)
	}

	// The directory is created on the way.
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("expected the directory to be created: %+v", err)
	}
}

func TestCursorAbsentOrUnreadable(t *testing.T) {
	if got := readCursor(""); got != "" {
		t.Errorf("expected no cursor when none is configured, got %q", got)
	}

	// A missing file is the normal first run, not a failure.
	if got := readCursor(filepath.Join(t.TempDir(), "nothing")); got != "" {
		t.Errorf("expected an empty cursor, got %q", got)
	}
}

// TestCursorKeepsUpWithABurst is the regression a real run exposed: a
// thousand entries arrive in well under a second, then the journal goes
// quiet. Writing only when an entry is recorded left the file holding
// the first line of the burst, so the next start replayed all of it.
func TestCursorKeepsUpWithABurst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor")

	writer := newCursorWriter(path)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)

		writer.run(ctx, 10*time.Millisecond)
	}()

	for i := range 1000 {
		writer.record("entry-" + string(rune('a'+i%26)) + "-" + time.Duration(i).String())
	}

	last := "entry-final"
	writer.record(last)

	// The journal then goes idle: nothing else is recorded, so only a
	// clock can move the cursor forward.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if readCursor(path) == last {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	if got := readCursor(path); got != last {
		t.Errorf("expected the newest position to reach the disk while idle, got %q", got)
	}

	cancel()
	<-done
}

// TestCursorFlushesOnShutdown keeps a clean stop from losing the last
// position.
func TestCursorFlushesOnShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor")

	writer := newCursorWriter(path)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)

		writer.run(ctx, time.Hour) // never ticks: only the shutdown writes
	}()

	writer.record("last-position")

	cancel()
	<-done

	if got := readCursor(path); got != "last-position" {
		t.Errorf("expected the pending cursor to be flushed on shutdown, got %q", got)
	}
}

// TestCursorSkipsRedundantWrites keeps an idle follower from rewriting
// the same value every second.
func TestCursorSkipsRedundantWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor")

	writer := newCursorWriter(path)
	writer.record("x")
	writer.flush()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	time.Sleep(10 * time.Millisecond)
	writer.flush()

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if !info.ModTime().Equal(after.ModTime()) {
		t.Error("expected an unchanged cursor not to be rewritten")
	}
}

// TestCursorWriterWithoutPath does nothing rather than failing: the
// cursor is optional.
func TestCursorWriterWithoutPath(t *testing.T) {
	writer := newCursorWriter("")
	writer.record("x")
	writer.flush()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	writer.run(ctx, time.Millisecond)
}
