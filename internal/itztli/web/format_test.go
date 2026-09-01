package web

import (
	"strings"
	"testing"
	"time"
)

func TestMaskParts(t *testing.T) {
	parts := MaskParts("HTTP server listening on <IP>:<NUM>")

	want := []Part{
		{Text: "HTTP server listening on "},
		{Text: "<IP>", Mask: true},
		{Text: ":"},
		{Text: "<NUM>", Mask: true},
	}

	if len(parts) != len(want) {
		t.Fatalf("got %d parts, want %d: %+v", len(parts), len(want), parts)
	}

	for i := range want {
		if parts[i] != want[i] {
			t.Errorf("part %d = %+v, want %+v", i, parts[i], want[i])
		}
	}
}

func TestMaskPartsPlain(t *testing.T) {
	parts := MaskParts("no mask here")
	if len(parts) != 1 || parts[0].Mask || parts[0].Text != "no mask here" {
		t.Fatalf("unexpected parts: %+v", parts)
	}
}

func TestFrenchDuration(t *testing.T) {
	for _, test := range []struct {
		in   time.Duration
		want string
	}{
		{20 * time.Second, "20 secondes"},
		{1 * time.Second, "1 seconde"},
		{59 * time.Minute, "59 minutes"},
		{time.Minute, "1 minute"},
		// 59m59.6s must not read "59 minutes 60 secondes".
		{59*time.Minute + 45*time.Second, "1 heure"},
		{time.Hour + 40*time.Minute, "1 heure 40 min"},
		{2 * time.Hour, "2 heures"},
		{26 * time.Hour, "1 jour 2 h"},
		{48 * time.Hour, "2 jours"},
		{-3 * time.Minute, "3 minutes"},
	} {
		if got := FrenchDuration(test.in); got != test.want {
			t.Errorf("FrenchDuration(%s) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestFormatFloat(t *testing.T) {
	for _, test := range []struct {
		in   float64
		want string
	}{
		{13.74, "13,74"},
		{0.064, "0,064"},
		{0.99, "0,99"},
		{1.68, "1,68"},
		{2840, "2\u202f840"},
		{0, "0"},
		{-0.5, "-0,5"},
	} {
		if got := FormatFloat(test.in); got != test.want {
			t.Errorf("FormatFloat(%g) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestFormatInt(t *testing.T) {
	for _, test := range []struct {
		in   int64
		want string
	}{
		{608, "608"},
		{11423, "11\u202f423"},
		{1234567, "1\u202f234\u202f567"},
		{-4200, "-4\u202f200"},
	} {
		if got := FormatInt(test.in); got != test.want {
			t.Errorf("FormatInt(%d) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestAbsPeriodSameDay(t *testing.T) {
	zone := time.FixedZone("UTC+2", 2*3600)
	start := time.Date(2026, 9, 1, 23, 6, 9, 0, zone)
	end := start.Add(30 * time.Minute)

	got := AbsPeriod(start.UTC(), end.UTC())

	// The rendering uses the process's local zone; only assert the
	// stable parts.
	if !strings.Contains(got, "→") || !strings.Contains(got, "(UTC") || !strings.Contains(got, "2026") {
		t.Errorf("AbsPeriod = %q, missing span, zone or date", got)
	}
}
