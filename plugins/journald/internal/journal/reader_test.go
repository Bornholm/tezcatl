package journal

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestArgsPositioning(t *testing.T) {
	// Without a position, start at the tail: replaying a whole journal
	// into the detectors would flood them with history they will never
	// see again.
	if got := strings.Join(Args(Options{Priority: -1}), " "); !strings.Contains(got, "--lines=0") {
		t.Errorf("expected a tail start, got %s", got)
	}

	// A cursor is exact, so it wins over any time window.
	args := strings.Join(Args(Options{Priority: -1, Cursor: "s=1f28;i=66", Since: "1h ago"}), " ")
	if !strings.Contains(args, "--after-cursor=s=1f28;i=66") {
		t.Errorf("expected the cursor to be used, got %s", args)
	}

	if strings.Contains(args, "--since") || strings.Contains(args, "--lines=0") {
		t.Errorf("expected the cursor to replace any other position, got %s", args)
	}

	if got := strings.Join(Args(Options{Priority: -1, Since: "1h ago"}), " "); !strings.Contains(got, "--since=1h ago") {
		t.Errorf("expected the time window, got %s", got)
	}
}

func TestArgsFilters(t *testing.T) {
	args := strings.Join(Args(Options{
		Units:    []string{"nginx.service", "", "tezcatl-server.service"},
		Priority: 4,
		User:     true,
		Follow:   true,
	}), " ")

	for _, expected := range []string{
		"--output=json", "--no-pager", "--follow", "--user",
		"--unit=nginx.service", "--unit=tezcatl-server.service", "--priority=4",
	} {
		if !strings.Contains(args, expected) {
			t.Errorf("expected %q in %s", expected, args)
		}
	}

	// A negative priority means no filter at all.
	if got := strings.Join(Args(Options{Priority: -1}), " "); strings.Contains(got, "--priority") {
		t.Errorf("expected no priority filter, got %s", got)
	}
}

// TestReaderStreamsEntries feeds a canned journal so the reader is
// tested without a systemd on the machine.
func TestReaderStreamsEntries(t *testing.T) {
	stream := strings.Join([]string{
		`{"MESSAGE":"first","PRIORITY":"6","_SYSTEMD_UNIT":"nginx.service","__REALTIME_TIMESTAMP":"1788083524641026","__CURSOR":"c1"}`,
		`{"_SYSTEMD_UNIT":"nginx.service","__REALTIME_TIMESTAMP":"1788083524641027"}`,
		`garbage that is not json`,
		`{"MESSAGE":"second","PRIORITY":"3","_SYSTEMD_UNIT":"nginx.service","__REALTIME_TIMESTAMP":"1788083524641028","__CURSOR":"c2"}`,
	}, "\n")

	reader := NewReader(Options{Priority: -1})
	SetStarter(reader, func(ctx context.Context, args []string) (io.ReadCloser, func() error, error) {
		return io.NopCloser(strings.NewReader(stream)), nil, nil
	})

	entries := []Entry{}

	err := reader.Read(context.Background(), func(entry Entry) error {
		entries = append(entries, entry)

		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	// The record without a message and the malformed line are skipped,
	// without ending the stream.
	if len(entries) != 2 {
		t.Fatalf("expected 2 usable entries, got %d", len(entries))
	}

	if entries[0].Message != "first" || entries[1].Level != "error" {
		t.Errorf("unexpected entries: %+v", entries)
	}

	if entries[1].Cursor != "c2" {
		t.Errorf("expected the cursor of the last entry, got %q", entries[1].Cursor)
	}
}

// TestReaderStopsOnHandlerError keeps a failing emit from being
// swallowed: the host restarts the plugin, and losing the error would
// leave it spinning silently.
func TestReaderStopsOnHandlerError(t *testing.T) {
	reader := NewReader(Options{Priority: -1})
	SetStarter(reader, func(ctx context.Context, args []string) (io.ReadCloser, func() error, error) {
		return io.NopCloser(strings.NewReader(
			`{"MESSAGE":"x","__REALTIME_TIMESTAMP":"1"}` + "\n" + `{"MESSAGE":"y","__REALTIME_TIMESTAMP":"2"}`)), nil, nil
	})

	count := 0

	err := reader.Read(context.Background(), func(entry Entry) error {
		count++

		return io.ErrClosedPipe
	})

	if err == nil {
		t.Fatal("expected the handler error to be reported")
	}

	if count != 1 {
		t.Errorf("expected the stream to stop on the first error, got %d calls", count)
	}
}

// TestReaderHandlesLongLines covers stack traces: the scanner's default
// limit would end the stream on the first big entry.
func TestReaderHandlesLongLines(t *testing.T) {
	long := strings.Repeat("a", 300*1024)

	reader := NewReader(Options{Priority: -1})
	SetStarter(reader, func(ctx context.Context, args []string) (io.ReadCloser, func() error, error) {
		return io.NopCloser(strings.NewReader(
			`{"MESSAGE":"` + long + `","__REALTIME_TIMESTAMP":"1"}`)), nil, nil
	})

	var got Entry

	if err := reader.Read(context.Background(), func(entry Entry) error {
		got = entry

		return nil
	}); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(got.Message) != len(long) {
		t.Errorf("expected the whole message, got %d bytes", len(got.Message))
	}
}
