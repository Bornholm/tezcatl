package humanize

import (
	"testing"
	"time"
)

func TestDuration(t *testing.T) {
	for _, test := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{300 * time.Millisecond, "0.3s"},
		{6 * time.Second, "6s"},
		{9500 * time.Millisecond, "9.5s"},
		{45 * time.Second, "45s"},
		{time.Minute, "1m"},
		{63 * time.Second, "1m 3s"},
		// The dogfooding instance's own numbers, which is what the
		// whole package exists for.
		{1667700 * time.Millisecond, "27m 48s"},
		{6002 * time.Second, "1h 40m"},
		{2 * time.Hour, "2h"},
		{24 * time.Hour, "1d"},
		{25 * time.Hour, "1d 1h"},
		{68788 * time.Second, "19h 6m"},
		{-180 * time.Second, "-3m"},
	} {
		if got := Duration(test.in); got != test.want {
			t.Errorf("Duration(%s) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestSeconds(t *testing.T) {
	for _, test := range []struct {
		in   float64
		want string
	}{
		{0.3, "0.3s"},
		{6002, "1h 40m"},
		{14659, "4h 4m"},
	} {
		if got := Seconds(test.in); got != test.want {
			t.Errorf("Seconds(%g) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestSignedDuration(t *testing.T) {
	for _, test := range []struct {
		in   time.Duration
		want string
	}{
		{20 * time.Second, "+20s"},
		{-180 * time.Second, "-3m"},
		{0, "+0s"},
	} {
		if got := SignedDuration(test.in); got != test.want {
			t.Errorf("SignedDuration(%s) = %q, want %q", test.in, got, test.want)
		}
	}
}

// TestDurationNeverSwallowsATinyInterval guards the difference between
// "no data" and "faster than the display": a blog's access log with a
// mean interval of 40 ms must not read as 0s.
func TestDurationNeverSwallowsATinyInterval(t *testing.T) {
	for _, in := range []time.Duration{time.Millisecond, 40 * time.Millisecond, 49 * time.Millisecond} {
		if got := Duration(in); got != "<0.1s" {
			t.Errorf("Duration(%s) = %q, want %q", in, got, "<0.1s")
		}
	}

	if got := Duration(0); got != "0s" {
		t.Errorf("Duration(0) = %q, want %q", got, "0s")
	}
}
