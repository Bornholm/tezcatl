package sink

import (
	"context"
	"sync"

	"github.com/bornholm/tezcatl/internal/core/model"
)

// DefaultHistorySize is how many recent events the broadcast sink keeps
// for subscribers that connect after the fact.
const DefaultHistorySize = 500

// Broadcast keeps the most recent events in memory and fans them out to
// live subscribers. It exists for interactive inspection ("tezcatl
// top"), not for delivery: it never blocks the pipeline, and a
// subscriber that cannot keep up loses events rather than slowing
// detection down.
type Broadcast struct {
	mu sync.RWMutex
	// history is a ring buffer of the last size events, oldest first
	// once it has wrapped.
	history []model.Event
	next    int
	filled  bool

	subscribers map[int]chan model.Event
	nextID      int
	closed      bool
}

func NewBroadcast(size int) *Broadcast {
	if size <= 0 {
		size = DefaultHistorySize
	}

	return &Broadcast{
		history:     make([]model.Event, size),
		subscribers: map[int]chan model.Event{},
	}
}

func (b *Broadcast) Publish(ctx context.Context, events []model.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil
	}

	for _, event := range events {
		b.history[b.next] = event
		b.next = (b.next + 1) % len(b.history)

		if b.next == 0 {
			b.filled = true
		}

		for _, subscriber := range b.subscribers {
			select {
			case subscriber <- event:
			default:
				// The subscriber is behind; dropping is the only
				// option that keeps the pipeline moving.
			}
		}
	}

	return nil
}

// Subscribe returns the last history events (at most historySize) and a
// channel carrying the ones published afterwards. The returned function
// cancels the subscription and must be called.
func (b *Broadcast) Subscribe(historySize int, bufferSize int) ([]model.Event, <-chan model.Event, func()) {
	if bufferSize <= 0 {
		bufferSize = 64
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	events := make(chan model.Event, bufferSize)

	if b.closed {
		close(events)

		return nil, events, func() {}
	}

	id := b.nextID
	b.nextID++
	b.subscribers[id] = events

	return b.recent(historySize), events, func() {
		b.mu.Lock()
		defer b.mu.Unlock()

		if subscriber, exists := b.subscribers[id]; exists {
			delete(b.subscribers, id)
			close(subscriber)
		}
	}
}

// recent returns up to size events, oldest first. The caller holds the
// lock.
func (b *Broadcast) recent(size int) []model.Event {
	total := b.next
	if b.filled {
		total = len(b.history)
	}

	if size <= 0 || size > total {
		size = total
	}

	events := make([]model.Event, 0, size)
	for i := total - size; i < total; i++ {
		// Walk backwards from the write cursor so the oldest kept
		// event comes first, wrapping around the ring.
		index := (b.next - total + i + len(b.history)) % len(b.history)
		events = append(events, b.history[index])
	}

	return events
}

func (b *Broadcast) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil
	}

	b.closed = true

	for id, subscriber := range b.subscribers {
		delete(b.subscribers, id)
		close(subscriber)
	}

	return nil
}
