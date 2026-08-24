package processor

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
)

// ParseLog extracts structure from raw log lines before template mining:
// JSON envelopes (generic keys and journald -o json), normalized levels
// and event timestamps (JSON fields, leading RFC3339 token as produced
// by docker logs --timestamps). The mined message is the extracted
// payload, not the envelope; the raw line is preserved.
type ParseLog struct{}

func NewParseLog() *ParseLog {
	return &ParseLog{}
}

func (p *ParseLog) Name() string {
	return "parse-log"
}

var messageKeys = []string{"message", "msg", "log", "MESSAGE"}

var levelKeys = []string{"level", "severity", "lvl", "loglevel"}

var timeKeys = []string{"time", "ts", "timestamp", "@timestamp"}

func (p *ParseLog) Process(ctx context.Context, obs *model.Observation, emit port.EmitFunc) (bool, error) {
	if obs.Modality != model.ModalityLog || obs.Log == nil {
		return true, nil
	}

	line := strings.TrimSpace(obs.Log.Raw)

	if strings.HasPrefix(line, "{") {
		p.parseJSON(obs, line)
		return true, nil
	}

	p.parsePlain(obs, line)

	return true, nil
}

func (p *ParseLog) parseJSON(obs *model.Observation, line string) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		// Not actually JSON: mine the raw line.
		return
	}

	for _, key := range messageKeys {
		if message, ok := fields[key].(string); ok && message != "" {
			obs.Log.Message = strings.TrimSpace(message)
			break
		}
	}

	for _, key := range levelKeys {
		if level := normalizeLevel(fields[key]); level != "" {
			obs.Log.Level = level
			break
		}
	}

	// journald: PRIORITY is a syslog level ("0".."7").
	if obs.Log.Level == "" {
		if priority, ok := fields["PRIORITY"]; ok {
			obs.Log.Level = syslogLevel(priority)
		}
	}

	if obs.Timestamp.IsZero() {
		for _, key := range timeKeys {
			if timestamp, ok := parseTimeValue(fields[key]); ok {
				obs.Timestamp = timestamp
				break
			}
		}
	}

	// journald: __REALTIME_TIMESTAMP is epoch microseconds as a string.
	if obs.Timestamp.IsZero() {
		if raw, ok := fields["__REALTIME_TIMESTAMP"].(string); ok {
			if micros, err := strconv.ParseInt(raw, 10, 64); err == nil {
				obs.Timestamp = time.UnixMicro(micros)
			}
		}
	}
}

func (p *ParseLog) parsePlain(obs *model.Observation, line string) {
	token, rest, found := strings.Cut(line, " ")
	if !found {
		return
	}

	// docker logs --timestamps and many structured formats prefix the
	// line with an RFC3339 timestamp.
	if timestamp, err := time.Parse(time.RFC3339Nano, token); err == nil {
		if obs.Timestamp.IsZero() {
			obs.Timestamp = timestamp
		}

		obs.Log.Message = strings.TrimSpace(rest)
	}
}

func normalizeLevel(value any) string {
	level, ok := value.(string)
	if !ok {
		return ""
	}

	switch strings.ToLower(level) {
	case "trace", "debug":
		return "debug"
	case "info", "informational", "notice":
		return "info"
	case "warn", "warning":
		return "warn"
	case "error", "err":
		return "error"
	case "fatal", "critical", "crit", "panic", "emerg", "alert":
		return "fatal"
	default:
		return ""
	}
}

func syslogLevel(value any) string {
	var priority int64

	switch v := value.(type) {
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return ""
		}
		priority = parsed
	case float64:
		priority = int64(v)
	default:
		return ""
	}

	switch {
	case priority <= 2:
		return "fatal"
	case priority == 3:
		return "error"
	case priority == 4:
		return "warn"
	case priority <= 6:
		return "info"
	case priority == 7:
		return "debug"
	default:
		return ""
	}
}

func parseTimeValue(value any) (time.Time, bool) {
	switch v := value.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05.999999999Z0700"} {
			if timestamp, err := time.Parse(layout, v); err == nil {
				return timestamp, true
			}
		}

		return time.Time{}, false

	case float64:
		// Epoch seconds (possibly fractional), milliseconds or
		// microseconds depending on magnitude.
		switch {
		case v > 1e14:
			return time.UnixMicro(int64(v)), true
		case v > 1e11:
			return time.UnixMilli(int64(v)), true
		case v > 1e8:
			seconds := int64(v)
			nanos := int64((v - float64(seconds)) * 1e9)
			return time.Unix(seconds, nanos), true
		default:
			return time.Time{}, false
		}

	default:
		return time.Time{}, false
	}
}
