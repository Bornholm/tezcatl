package journal

import (
	"testing"
	"time"
)

func TestDecodeRealEntry(t *testing.T) {
	// Shaped after a real record: every value is a string, including
	// the numbers, so a 64-bit timestamp survives JSON intact.
	line := []byte(`{
		"MESSAGE":"Failed to start unit",
		"PRIORITY":"3",
		"__REALTIME_TIMESTAMP":"1788083524641026",
		"_SOURCE_REALTIME_TIMESTAMP":"1788083524640943",
		"_SYSTEMD_UNIT":"nginx.service",
		"SYSLOG_IDENTIFIER":"nginx",
		"_COMM":"nginx",
		"_HOSTNAME":"wpetit-thinkpad",
		"_PID":"5968",
		"__CURSOR":"s=1f28;i=6654a2;b=ab40"
	}`)

	entry, ok := Decode(line)
	if !ok {
		t.Fatal("expected the entry to be usable")
	}

	if entry.Message != "Failed to start unit" {
		t.Errorf("unexpected message: %q", entry.Message)
	}

	if entry.Level != "error" {
		t.Errorf("expected priority 3 to be an error, got %q", entry.Level)
	}

	// The application's own timestamp wins over the journal's arrival
	// time; they differ by microseconds here.
	if entry.Timestamp.UnixMicro() != 1788083524640943 {
		t.Errorf("expected the source timestamp, got %d", entry.Timestamp.UnixMicro())
	}

	if entry.Cursor == "" {
		t.Error("expected the cursor to be kept for resuming")
	}
}

func TestDecodeFallsBackOnJournalTimestamp(t *testing.T) {
	entry, ok := Decode([]byte(`{"MESSAGE":"x","__REALTIME_TIMESTAMP":"1788083524641026"}`))
	if !ok {
		t.Fatal("expected a usable entry")
	}

	if entry.Timestamp.UnixMicro() != 1788083524641026 {
		t.Errorf("expected the journal timestamp, got %s", entry.Timestamp)
	}
}

// TestDecodeBinaryMessage covers a case the real journal produces: a
// message that is not valid UTF-8 is exported as an array of byte
// values, not a string. Thirty-two of three thousand entries on the
// development machine were of that shape.
func TestDecodeBinaryMessage(t *testing.T) {
	entry, ok := Decode([]byte(`{"MESSAGE":[104,101,108,108,111],"PRIORITY":"6","__REALTIME_TIMESTAMP":"1788083524641026"}`))
	if !ok {
		t.Fatal("expected the byte array to be decoded")
	}

	if entry.Message != "hello" {
		t.Errorf("expected the bytes to become text, got %q", entry.Message)
	}
}

// TestDecodeRepeatedField covers the other array shape: a field that
// appeared several times in one entry.
func TestDecodeRepeatedField(t *testing.T) {
	entry, ok := Decode([]byte(`{"MESSAGE":"x","_SYSTEMD_UNIT":["a.service","b.service"],"__REALTIME_TIMESTAMP":"1"}`))
	if !ok {
		t.Fatal("expected a usable entry")
	}

	if entry.Unit != "a.service b.service" {
		t.Errorf("expected both values to be kept, got %q", entry.Unit)
	}
}

func TestDecodeSkipsEntriesWithoutMessage(t *testing.T) {
	if _, ok := Decode([]byte(`{"_SYSTEMD_UNIT":"nginx.service","__REALTIME_TIMESTAMP":"1"}`)); ok {
		t.Error("expected an entry with no message to be skipped")
	}

	if _, ok := Decode([]byte("not json")); ok {
		t.Error("expected a malformed line to be skipped")
	}
}

func TestServiceNaming(t *testing.T) {
	cases := []struct {
		entry    Entry
		expected string
	}{
		{Entry{Unit: "nginx.service"}, "nginx"},
		{Entry{Unit: "session-3.scope"}, "session-3"},
		// A templated unit keeps its instance: that is what an operator
		// recognizes.
		{Entry{Unit: "tezcatl-ingest@blog.service"}, "tezcatl-ingest@blog"},
		{Entry{Identifier: "gsconnect", Comm: "gjs"}, "gsconnect"},
		{Entry{Comm: "gjs"}, "gjs"},
		{Entry{}, ""},
	}

	for _, tc := range cases {
		if got := tc.entry.Service(); got != tc.expected {
			t.Errorf("expected %q, got %q for %+v", tc.expected, got, tc.entry)
		}
	}
}

func TestPriorityToLevel(t *testing.T) {
	cases := map[string]string{
		"0": "fatal", "2": "fatal", "3": "error", "4": "warn",
		"5": "info", "6": "info", "7": "debug", "": "", "x": "",
	}

	for priority, expected := range cases {
		if got := priorityToLevel(priority); got != expected {
			t.Errorf("priority %q: expected %q, got %q", priority, expected, got)
		}
	}
}

func TestMicroseconds(t *testing.T) {
	if got := microseconds("1788083524641026"); !got.Equal(time.UnixMicro(1788083524641026)) {
		t.Errorf("unexpected time: %s", got)
	}

	if got := microseconds("nope"); !got.IsZero() {
		t.Errorf("expected a zero time for a malformed value, got %s", got)
	}
}
