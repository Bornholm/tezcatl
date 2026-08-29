package sink

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func event(id string) model.Event {
	return model.Event{ID: id, Kind: "anomaly.log", Source: "prod/api"}
}

func publish(t *testing.T, b *Broadcast, ids ...string) {
	t.Helper()

	events := make([]model.Event, 0, len(ids))
	for _, id := range ids {
		events = append(events, event(id))
	}

	if err := b.Publish(context.Background(), events); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
}

func ids(events []model.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.ID)
	}

	return out
}

func TestBroadcastHistory(t *testing.T) {
	b := NewBroadcast(3)
	defer b.Close()

	publish(t, b, "a", "b")

	history, _, cancel := b.Subscribe(0, 0)
	cancel()

	if got := fmt.Sprint(ids(history)); got != "[a b]" {
		t.Errorf("expected [a b], got %s", got)
	}

	// Wrapping keeps the newest events, oldest first.
	publish(t, b, "c", "d", "e")

	history, _, cancel = b.Subscribe(0, 0)
	cancel()

	if got := fmt.Sprint(ids(history)); got != "[c d e]" {
		t.Errorf("expected [c d e] after wrapping, got %s", got)
	}

	history, _, cancel = b.Subscribe(2, 0)
	cancel()

	if got := fmt.Sprint(ids(history)); got != "[d e]" {
		t.Errorf("expected the 2 newest events, got %s", got)
	}
}

func TestBroadcastLiveDelivery(t *testing.T) {
	b := NewBroadcast(10)
	defer b.Close()

	publish(t, b, "old")

	history, events, cancel := b.Subscribe(0, 4)
	defer cancel()

	if got := fmt.Sprint(ids(history)); got != "[old]" {
		t.Fatalf("expected [old] as history, got %s", got)
	}

	publish(t, b, "live")

	select {
	case received := <-events:
		if received.ID != "live" {
			t.Errorf("expected the live event, got %s", received.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("live event never delivered")
	}

	cancel()

	if _, open := <-events; open {
		t.Error("expected the channel to be closed after cancelling")
	}
}

// TestBroadcastNeverBlocks is the property that matters: a subscriber
// that stops reading must not stall the pipeline.
func TestBroadcastNeverBlocks(t *testing.T) {
	b := NewBroadcast(4)
	defer b.Close()

	_, _, cancel := b.Subscribe(0, 1)
	defer cancel()

	done := make(chan struct{})

	go func() {
		defer close(done)

		for i := range 1000 {
			publish(t, b, fmt.Sprintf("event-%d", i))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked on a subscriber that stopped reading")
	}
}

func TestBroadcastCloseUnblocksSubscribers(t *testing.T) {
	b := NewBroadcast(4)

	_, events, cancel := b.Subscribe(0, 1)
	defer cancel()

	if err := b.Close(); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	select {
	case _, open := <-events:
		if open {
			t.Error("expected the channel to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("closing left the subscriber hanging")
	}

	// Publishing after close is a no-op rather than a panic.
	publish(t, b, "after-close")
}
