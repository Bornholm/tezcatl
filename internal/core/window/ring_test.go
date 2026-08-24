package window

import (
	"strconv"
	"testing"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func TestRing(t *testing.T) {
	ring := NewRing(3)

	if got := ring.Last(3); got != nil {
		t.Fatalf("expected empty ring, got %d observations", len(got))
	}

	for i := range 5 {
		ring.Add(model.Observation{ID: strconv.Itoa(i)})
	}

	last := ring.Last(3)
	if len(last) != 3 {
		t.Fatalf("expected 3 observations, got %d", len(last))
	}

	for i, want := range []string{"2", "3", "4"} {
		if last[i].ID != want {
			t.Errorf("expected observation %q at index %d, got %q", want, i, last[i].ID)
		}
	}

	if got := ring.Last(2); len(got) != 2 || got[0].ID != "3" || got[1].ID != "4" {
		t.Fatalf("unexpected partial window: %+v", got)
	}
}
