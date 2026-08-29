package processor

import (
	"context"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

func parseLine(t *testing.T, line string) *model.Observation {
	t.Helper()

	obs := &model.Observation{
		Service:  "api",
		Modality: model.ModalityLog,
		Log:      &model.LogRecord{Raw: line},
	}

	next, err := NewParseLog().Process(context.Background(), obs, nil)
	if err != nil || !next {
		t.Fatalf("unexpected result: next=%v err=%+v", next, err)
	}

	return obs
}

func TestParseLogJSON(t *testing.T) {
	obs := parseLine(t, `{"time":"2026-08-24T14:02:00Z","level":"ERROR","msg":"database connection timeout","logger":"pool"}`)

	if obs.Log.Message != "database connection timeout" {
		t.Errorf("unexpected message: %q", obs.Log.Message)
	}

	if obs.Log.Level != "error" {
		t.Errorf("unexpected level: %q", obs.Log.Level)
	}

	want := time.Date(2026, 8, 24, 14, 2, 0, 0, time.UTC)
	if !obs.Timestamp.Equal(want) {
		t.Errorf("unexpected timestamp: %v", obs.Timestamp)
	}

	if obs.Log.EffectiveMessage() != "database connection timeout" {
		t.Errorf("unexpected effective message: %q", obs.Log.EffectiveMessage())
	}
}

func TestParseLogJSONEpochAndAliases(t *testing.T) {
	obs := parseLine(t, `{"ts":1787200920.5,"severity":"warning","message":"slow query detected"}`)

	if obs.Log.Message != "slow query detected" || obs.Log.Level != "warn" {
		t.Errorf("unexpected parse result: %+v", obs.Log)
	}

	if obs.Timestamp.Unix() != 1787200920 {
		t.Errorf("unexpected timestamp: %v", obs.Timestamp)
	}
}

// TestParseLogNamedKeys takes a feed whose envelope uses names of its
// own (here journald's) and shows that reading it is a matter of naming
// the keys, not of teaching the parser about a product. The defaults
// deliberately ignore it: see TestParseLogUnknownKeysIgnored.
func TestParseLogNamedKeys(t *testing.T) {
	line := `{"MESSAGE":"Failed to start unit","PRIORITY":"3","__REALTIME_TIMESTAMP":"1787200920000000","_SYSTEMD_UNIT":"app.service"}`

	obs := &model.Observation{Service: "api", Modality: model.ModalityLog, Log: &model.LogRecord{Raw: line}}

	parser := NewParseLog(
		WithMessageKeys("MESSAGE"),
		WithLevelKeys("PRIORITY"),
		WithTimeKeys("__REALTIME_TIMESTAMP"),
	)

	if _, err := parser.Process(context.Background(), obs, nil); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if obs.Log.Message != "Failed to start unit" {
		t.Errorf("unexpected message: %q", obs.Log.Message)
	}

	// "3" is a syslog priority, decoded from the shape of the value.
	if obs.Log.Level != "error" {
		t.Errorf("unexpected level: %q", obs.Log.Level)
	}

	// Epoch microseconds quoted as a string.
	if obs.Timestamp.Unix() != 1787200920 {
		t.Errorf("unexpected timestamp: %v", obs.Timestamp)
	}
}

// TestParseLogUnknownKeysIgnored states the flip side: the core ships
// the key names JSON loggers share, and nobody else's.
func TestParseLogUnknownKeysIgnored(t *testing.T) {
	obs := parseLine(t, `{"MESSAGE":"Failed to start unit","PRIORITY":"3"}`)

	if obs.Log.Message != "" || obs.Log.Level != "" {
		t.Errorf("expected a feed's own key names to need configuring, got %+v", obs.Log)
	}
}

// TestParseLogNumericLevel covers the value-shape decoding that made
// the product-specific branches unnecessary.
func TestParseLogNumericLevel(t *testing.T) {
	cases := map[string]string{
		`{"msg":"x","level":3}`:              "error",
		`{"msg":"x","level":"4"}`:            "warn",
		`{"msg":"x","level":"warning"}`:      "warn",
		`{"msg":"x","severity":7}`:           "debug",
		`{"msg":"x","level":"not-a-level"}`:  "",
		`{"msg":"x","time":"1787200920000"}`: "",
	}

	for line, expected := range cases {
		if got := parseLine(t, line).Log.Level; got != expected {
			t.Errorf("%s: expected level %q, got %q", line, expected, got)
		}
	}

	// An epoch quoted as a string is still a timestamp.
	if got := parseLine(t, `{"msg":"x","time":"1787200920000"}`).Timestamp.Unix(); got != 1787200920 {
		t.Errorf("expected a quoted epoch to be read, got %d", got)
	}
}

func TestParseLogDockerTimestampPrefix(t *testing.T) {
	obs := parseLine(t, "2026-08-24T14:02:00.123456789Z payment failed: timeout")

	if obs.Log.Message != "payment failed: timeout" {
		t.Errorf("unexpected message: %q", obs.Log.Message)
	}

	if obs.Timestamp.IsZero() || obs.Timestamp.Year() != 2026 {
		t.Errorf("unexpected timestamp: %v", obs.Timestamp)
	}
}

func TestParseLogPlainTextFallback(t *testing.T) {
	obs := parseLine(t, "plain message without any timestamp")

	if obs.Log.Message != "" {
		t.Errorf("expected raw line to stay the message, got %q", obs.Log.Message)
	}

	if !obs.Timestamp.IsZero() {
		t.Errorf("expected no timestamp, got %v", obs.Timestamp)
	}

	if obs.Log.EffectiveMessage() != "plain message without any timestamp" {
		t.Errorf("unexpected effective message: %q", obs.Log.EffectiveMessage())
	}
}

// TestParseLogDokku exercises real dokku logs output: ANSI color
// escapes, docker timestamp, process prefix, then the actual payload.
func TestParseLogDokku(t *testing.T) {
	t.Run("json payload", func(t *testing.T) {
		obs := parseLine(t, "\x1b[36m2026-08-25T09:41:38.016458925Z app[web.1]:\x1b[0m {\"level\":\"warn\",\"ts\":1787996498.5,\"logger\":\"http\",\"msg\":\"HTTP/3 skipped because it requires TLS\"}")

		if obs.Log.Message != "HTTP/3 skipped because it requires TLS" {
			t.Errorf("unexpected message: %q", obs.Log.Message)
		}

		if obs.Log.Level != "warn" {
			t.Errorf("unexpected level: %q", obs.Log.Level)
		}

		// The docker timestamp comes first, the JSON ts must not override it.
		want := time.Date(2026, 8, 25, 9, 41, 38, 16458925, time.UTC)
		if !obs.Timestamp.Equal(want) {
			t.Errorf("unexpected timestamp: %v", obs.Timestamp)
		}

		if obs.Attributes[AttrLogProcess] != "app[web.1]" {
			t.Errorf("unexpected process attribute: %q", obs.Attributes[AttrLogProcess])
		}
	})

	t.Run("plain payload", func(t *testing.T) {
		obs := parseLine(t, "2026-08-25T09:41:38Z app[web.1]: 10.0.0.1 - - \"GET /robots.txt HTTP/1.1\" 200 512")

		if obs.Log.Message != "10.0.0.1 - - \"GET /robots.txt HTTP/1.1\" 200 512" {
			t.Errorf("unexpected message: %q", obs.Log.Message)
		}

		if obs.Attributes[AttrLogProcess] != "app[web.1]" {
			t.Errorf("unexpected process attribute: %q", obs.Attributes[AttrLogProcess])
		}

		if obs.Timestamp.IsZero() {
			t.Error("expected timestamp to be extracted")
		}
	})

	t.Run("ansi only", func(t *testing.T) {
		obs := parseLine(t, "\x1b[0;33msome colored warning\x1b[0m")

		if obs.Log.Message != "some colored warning" {
			t.Errorf("expected escapes to be stripped, got %q", obs.Log.Message)
		}
	})

	t.Run("prefix without timestamp", func(t *testing.T) {
		obs := parseLine(t, "heroku[router]: at=info method=GET path=/")

		if obs.Log.Message != "at=info method=GET path=/" {
			t.Errorf("unexpected message: %q", obs.Log.Message)
		}

		if obs.Attributes[AttrLogProcess] != "heroku[router]" {
			t.Errorf("unexpected process attribute: %q", obs.Attributes[AttrLogProcess])
		}
	})
}

// TestParseLogKeepsSourceParsing covers the contract that lets a source
// own its own format: what it already extracted is taken as given, so
// the server never re-guesses a message it did not have to flatten.
func TestParseLogKeepsSourceParsing(t *testing.T) {
	cases := []struct {
		name    string
		record  *model.LogRecord
		message string
		level   string
	}{
		{
			name:    "message and level are kept",
			record:  &model.LogRecord{Raw: `{"MESSAGE":"disk almost full","PRIORITY":"4"}`, Message: "disk almost full", Level: "warn"},
			message: "disk almost full",
			level:   "warn",
		},
		{
			name:    "a level is mapped onto the normalized vocabulary",
			record:  &model.LogRecord{Raw: "whatever", Message: "boot finished", Level: "NOTICE"},
			message: "boot finished",
			level:   "info",
		},
		{
			name:    "an unrecognized level is kept rather than dropped",
			record:  &model.LogRecord{Raw: "whatever", Message: "boot finished", Level: "verbose"},
			message: "boot finished",
			level:   "verbose",
		},
		{
			name: "a message that looks like json is not unwrapped again",
			record: &model.LogRecord{
				Raw:     `{"msg":"outer"}`,
				Message: `{"msg":"outer"}`,
			},
			message: `{"msg":"outer"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := &model.Observation{Service: "api", Modality: model.ModalityLog, Log: tc.record}

			if _, err := NewParseLog().Process(context.Background(), obs, nil); err != nil {
				t.Fatalf("unexpected error: %+v", err)
			}

			if obs.Log.Message != tc.message {
				t.Errorf("expected message %q, got %q", tc.message, obs.Log.Message)
			}

			if obs.Log.Level != tc.level {
				t.Errorf("expected level %q, got %q", tc.level, obs.Log.Level)
			}
		})
	}
}

func TestParseLogMalformedJSON(t *testing.T) {
	obs := parseLine(t, `{"broken json`)

	if obs.Log.Message != "" || obs.Log.Level != "" {
		t.Errorf("expected malformed json to be left as raw, got %+v", obs.Log)
	}
}
