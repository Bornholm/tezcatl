package sink

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

type flakySink struct {
	mu        sync.Mutex
	failures  int
	published []model.Event
}

func (s *flakySink) Publish(ctx context.Context, events []model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failures > 0 {
		s.failures--
		return errors.New("destination unavailable")
	}

	s.published = append(s.published, events...)

	return nil
}

func (s *flakySink) Close() error { return nil }

func (s *flakySink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.published)
}

func TestResilientRetriesTransientFailures(t *testing.T) {
	inner := &flakySink{failures: 2}

	resilient := newResilient("test", inner, 16, 5, 10*time.Millisecond)

	if err := resilient.Publish(context.Background(), []model.Event{{ID: "evt-1"}}); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	// Publish must never block, even while the inner sink fails.
	done := make(chan struct{})
	go func() {
		defer close(done)
		resilient.Publish(context.Background(), []model.Event{{ID: "evt-2"}})
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on failing sink")
	}

	if err := resilient.Close(); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if count := inner.count(); count != 2 {
		t.Fatalf("expected 2 events published after retries, got %d", count)
	}
}

func TestResilientDropsWhenQueueFull(t *testing.T) {
	inner := &flakySink{failures: 1000}

	resilient := newResilient("test", inner, 1, 2, 10*time.Millisecond)

	for i := range 10 {
		resilient.Publish(context.Background(), []model.Event{{ID: model.NewID(), Confidence: float64(i)}})
	}

	if resilient.Dropped() == 0 {
		t.Fatal("expected events to be dropped when the queue is full")
	}
}
