package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/gdamore/tcell/v2"
	"github.com/pkg/errors"
)

// fakeSource drives the view without a server: each Events call takes
// the next scripted session off the list.
type fakeSource struct {
	mu       sync.Mutex
	sessions []func(ctx context.Context, out chan<- model.Event, connected func()) error
	calls    int
	history  []model.Event
}

func (s *fakeSource) ListEvents(ctx context.Context, limit int) ([]model.Event, error) {
	return s.history, nil
}

func (s *fakeSource) Templates(ctx context.Context) ([]admin.TemplateInfo, error) {
	return nil, nil
}

func (s *fakeSource) Metrics(ctx context.Context) ([]detect.SeriesInfo, error) {
	return nil, nil
}

func (s *fakeSource) Mark(ctx context.Context, template string, marking detect.Marking) error {
	return nil
}

func (s *fakeSource) MarkMetric(ctx context.Context, pattern string, ignore bool) error {
	return nil
}

func (s *fakeSource) Events(ctx context.Context, history int, out chan<- model.Event, connected func()) error {
	s.mu.Lock()
	index := s.calls
	s.calls++
	s.mu.Unlock()

	if index < len(s.sessions) {
		return s.sessions[index](ctx, out, connected)
	}

	<-ctx.Done()

	return nil
}

// runView starts the view on a simulation screen, so QueueUpdateDraw
// has a running application to hand its updates to.
func runView(t *testing.T, source Source) (*top, func()) {
	t.Helper()

	view := &top{source: source, opts: Options{Refresh: time.Hour}}
	view.build()
	view.app.SetScreen(tcell.NewSimulationScreen("UTF-8"))

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)

		_ = view.app.Run()
	}()

	go view.streamLoop(ctx)

	return view, func() {
		cancel()
		view.app.Stop()
		<-done
	}
}

func (t *top) streamingNow() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.streaming
}

func (t *top) eventCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return len(t.events)
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

// TestStreamLoopReportsDisconnection covers what a terminal session
// showed late: the status must stop claiming "live" the moment the
// stream ends, and must come back on its own afterwards.
func TestStreamLoopReportsDisconnection(t *testing.T) {
	previous := eventReconnectDelay
	eventReconnectDelay = 50 * time.Millisecond
	defer func() { eventReconnectDelay = previous }()

	died := make(chan struct{})

	source := &fakeSource{
		sessions: []func(ctx context.Context, out chan<- model.Event, connected func()) error{
			// First session: connects, delivers one event, then the
			// server goes away.
			func(ctx context.Context, out chan<- model.Event, connected func()) error {
				connected()
				out <- model.Event{ID: "e1", Kind: "anomaly.log", Source: "prod/api", Timestamp: time.Now()}
				<-died

				return errors.New("connection reset")
			},
			// Second session: the server is still down.
			func(ctx context.Context, out chan<- model.Event, connected func()) error {
				return errors.New("connection refused")
			},
			// Third session: it is back.
			func(ctx context.Context, out chan<- model.Event, connected func()) error {
				connected()
				<-ctx.Done()

				return nil
			},
		},
	}

	view, stop := runView(t, source)
	defer stop()

	waitFor(t, "the stream to be reported live", view.streamingNow)
	waitFor(t, "the event to reach the view", func() bool { return view.eventCount() == 1 })

	close(died)

	waitFor(t, "the disconnection to be reported", func() bool { return !view.streamingNow() })
	waitFor(t, "the stream to come back on its own", view.streamingNow)

	// The event received before the disconnection is still there.
	if got := view.eventCount(); got != 1 {
		t.Errorf("expected the view to keep its events across a reconnection, got %d", got)
	}
}

// TestStreamLoopDedupsHistoryAndLive covers the deliberate overlap
// between the persistent history and the live ring: the same event must
// not appear twice in the view.
func TestStreamLoopDedupsHistoryAndLive(t *testing.T) {
	base := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	source := &fakeSource{
		history: []model.Event{
			{ID: "e1", Kind: "anomaly.log", Source: "prod/api", Timestamp: base},
		},
		sessions: []func(ctx context.Context, out chan<- model.Event, connected func()) error{
			func(ctx context.Context, out chan<- model.Event, connected func()) error {
				connected()
				// The ring replays e1 too, then a fresh event arrives.
				out <- model.Event{ID: "e1", Kind: "anomaly.log", Source: "prod/api", Timestamp: base}
				out <- model.Event{ID: "e2", Kind: "anomaly.log", Source: "prod/api", Timestamp: base.Add(time.Minute)}
				<-ctx.Done()

				return nil
			},
		},
	}

	view, stop := runView(t, source)
	defer stop()

	waitFor(t, "both events to be visible", func() bool { return view.eventCount() == 2 })

	// Give the duplicate a chance to slip in before asserting.
	time.Sleep(50 * time.Millisecond)

	if got := view.eventCount(); got != 2 {
		t.Errorf("expected e1 to be deduplicated, got %d events", got)
	}
}

// TestRenderEventsStatus checks what the status line actually says,
// since that is the part a user reads to trust the feed.
func TestRenderEventsStatus(t *testing.T) {
	view := &top{source: &fakeSource{}, opts: Options{Refresh: time.Hour}}
	view.build()

	view.mu.Lock()
	view.events = []model.Event{{ID: "e1", Kind: "anomaly.log", Source: "prod/api", Severity: model.SeverityCritical, Summary: "boom", Timestamp: time.Now()}}
	view.mu.Unlock()

	view.render()

	if status := view.status.GetText(true); !strings.Contains(status, "1 events (disconnected)") {
		t.Errorf("expected a disconnected status, got %q", status)
	}

	view.mu.Lock()
	view.streaming = true
	view.mu.Unlock()

	view.render()

	if status := view.status.GetText(true); !strings.Contains(status, "1 events (live)") {
		t.Errorf("expected a live status, got %q", status)
	}

	// Row 0 is the header, so the event lands on row 1.
	if cell := view.eventsTable.GetCell(1, 4); cell == nil || cell.Text != "boom" {
		t.Errorf("expected the summary in the last column, got %+v", cell)
	}
}
