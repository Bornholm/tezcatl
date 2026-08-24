package engine

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
)

type staticIngester struct {
	observations []model.Observation
}

func (i *staticIngester) Ingest(ctx context.Context, out chan<- model.Observation) error {
	for _, obs := range i.observations {
		select {
		case out <- obs:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

type blockingIngester struct{}

func (i *blockingIngester) Ingest(ctx context.Context, out chan<- model.Observation) error {
	<-ctx.Done()
	return ctx.Err()
}

type debugProcessor struct{}

func (p *debugProcessor) Name() string { return "debug" }

func (p *debugProcessor) Process(ctx context.Context, obs *model.Observation, emit port.EmitFunc) (bool, error) {
	emit(model.Event{
		ID:     model.NewID(),
		Kind:   "debug.observation",
		Source: obs.Source,
		Attributes: map[string]string{
			"sequence": obs.Attributes["sequence"],
		},
	})

	return true, nil
}

type memorySink struct {
	mu     sync.Mutex
	events []model.Event
}

func (s *memorySink) Publish(ctx context.Context, events []model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events = append(s.events, events...)

	return nil
}

func (s *memorySink) Close() error { return nil }

func TestEngineProcessesAllObservations(t *testing.T) {
	const sources = 4
	const perSource = 250

	observations := make([]model.Observation, 0, sources*perSource)
	for s := range sources {
		for n := range perSource {
			observations = append(observations, model.Observation{
				ID:       model.NewID(),
				Source:   fmt.Sprintf("source-%d", s),
				Modality: model.ModalityLog,
				Attributes: map[string]string{
					"sequence": strconv.Itoa(n),
				},
				Log: &model.LogRecord{Raw: "line"},
			})
		}
	}

	sink := &memorySink{}

	e := New(
		WithIngesters(&staticIngester{observations: observations}),
		WithProcessors(&debugProcessor{}),
		WithSinks(sink),
		WithWorkers(3),
		WithObservationBufferSize(16),
		WithEventBufferSize(16),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.Run(ctx); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if got, want := len(sink.events), sources*perSource; got != want {
		t.Fatalf("expected %d events, got %d", want, got)
	}

	// Observations of a same partition must be processed in ingestion order.
	lastSequence := map[string]int{}
	for _, evt := range sink.events {
		sequence, err := strconv.Atoi(evt.Attributes["sequence"])
		if err != nil {
			t.Fatalf("unexpected sequence attribute: %+v", err)
		}

		if last, exists := lastSequence[evt.Source]; exists && sequence <= last {
			t.Fatalf("out-of-order event for source %q: sequence %d after %d", evt.Source, sequence, last)
		}

		lastSequence[evt.Source] = sequence
	}
}

type slowSink struct {
	memorySink
	delay time.Duration
}

func (s *slowSink) Publish(ctx context.Context, events []model.Event) error {
	time.Sleep(s.delay)

	return s.memorySink.Publish(ctx, events)
}

// TestEngineBackpressure verifies that a slow sink slows ingestion down
// through the bounded channels instead of losing events or deadlocking,
// and that buffered work is drained at shutdown.
func TestEngineBackpressure(t *testing.T) {
	const total = 300

	observations := make([]model.Observation, 0, total)
	for i := range total {
		observations = append(observations, model.Observation{
			ID:       model.NewID(),
			Source:   fmt.Sprintf("source-%d", i%3),
			Modality: model.ModalityLog,
			Log:      &model.LogRecord{Raw: "line"},
		})
	}

	sink := &slowSink{delay: time.Millisecond}

	e := New(
		WithIngesters(&staticIngester{observations: observations}),
		WithProcessors(&debugProcessor{}),
		WithSinks(sink),
		WithWorkers(2),
		WithObservationBufferSize(4),
		WithEventBufferSize(4),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	before := runtime.NumGoroutine()

	if err := e.Run(ctx); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if got := len(sink.events); got != total {
		t.Fatalf("expected %d events, got %d", total, got)
	}

	// All engine goroutines must be gone once Run returns.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutine leak: %d before, %d after", before, after)
	}
}

func TestEngineStopsOnContextCancellation(t *testing.T) {
	e := New(
		WithIngesters(&blockingIngester{}),
		WithProcessors(&debugProcessor{}),
		WithSinks(&memorySink{}),
	)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- e.Run(ctx)
	}()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation error, got %+v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not stop after context cancellation")
	}
}
