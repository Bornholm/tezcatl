package processor

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/bornholm/tezcatl/internal/core/port"
)

// ParseLog unwraps a log line into a message, a level and a timestamp
// before template mining, preserving the raw line. The line is peeled in
// order: ANSI escapes, a leading RFC3339 timestamp, a bracketed emitter
// prefix, then a JSON envelope when the payload is one.
//
// It knows shapes, not products: a JSON envelope, an RFC3339 prefix, a
// bracketed emitter tag, a numeric level read as a syslog priority. The
// field names it looks for are data rather than code, so ingesting a
// feed that names them differently is a matter of configuration, and
// teaching tezcatl a format never means editing this file.
type ParseLog struct {
	messageKeys []string
	levelKeys   []string
	timeKeys    []string
}

type ParseLogOptionFunc func(p *ParseLog)

// WithMessageKeys overrides the JSON keys holding the message.
func WithMessageKeys(keys ...string) ParseLogOptionFunc {
	return func(p *ParseLog) {
		if len(keys) > 0 {
			p.messageKeys = keys
		}
	}
}

// WithLevelKeys overrides the JSON keys holding the level.
func WithLevelKeys(keys ...string) ParseLogOptionFunc {
	return func(p *ParseLog) {
		if len(keys) > 0 {
			p.levelKeys = keys
		}
	}
}

// WithTimeKeys overrides the JSON keys holding the timestamp.
func WithTimeKeys(keys ...string) ParseLogOptionFunc {
	return func(p *ParseLog) {
		if len(keys) > 0 {
			p.timeKeys = keys
		}
	}
}

func NewParseLog(opts ...ParseLogOptionFunc) *ParseLog {
	p := &ParseLog{
		messageKeys: DefaultMessageKeys(),
		levelKeys:   DefaultLevelKeys(),
		timeKeys:    DefaultTimeKeys(),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

func (p *ParseLog) Name() string {
	return "parse-log"
}

// AttrLogProcess carries the emitter tag of a bracketed prefix, the
// shape PaaS log drains and syslog share (e.g. "app[web.1]").
const AttrLogProcess = "log.process"

// The default key sets stay to what JSON loggers have in common. A feed
// with names of its own (journald's MESSAGE, PRIORITY and
// __REALTIME_TIMESTAMP, for instance) is a matter of adding them in
// logs.parsing, or better, of a source plugin filling the message and
// the level itself.
func DefaultMessageKeys() []string {
	return []string{"message", "msg", "log"}
}

func DefaultLevelKeys() []string {
	return []string{"level", "severity", "lvl", "loglevel"}
}

func DefaultTimeKeys() []string {
	return []string{"time", "ts", "timestamp", "@timestamp"}
}

// ansiEscape matches CSI sequences (colors and cursor controls), which
// log tails keep even when nothing is attached to a terminal.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[@-~]`)

// processPrefix matches a bracketed emitter prefix such as
// "app[web.1]: " or "router[http]: ". Keep it a shape, not a list of
// emitters: a new product is not a reason to add a case here.
var processPrefix = regexp.MustCompile(`^([A-Za-z0-9_.-]+)\[([^\]\s]+)\]:\s+`)

func (p *ParseLog) Process(ctx context.Context, obs *model.Observation, emit port.EmitFunc) (bool, error) {
	if obs.Modality != model.ModalityLog || obs.Log == nil {
		return true, nil
	}

	// A source that read its logs from a structured feed knows the
	// message better than any heuristic applied to a flattened line.
	// Parsing it again would only be a chance to get it wrong.
	if obs.Log.Message != "" {
		// Map the level onto the normalized vocabulary when it fits,
		// and keep the source's own word when it does not: an
		// unrecognized level is still more than no level.
		if normalized := normalizeLevel(obs.Log.Level); normalized != "" {
			obs.Log.Level = normalized
		}

		return true, nil
	}

	line := strings.TrimSpace(obs.Log.Raw)

	if strings.IndexByte(line, '\x1b') >= 0 {
		line = strings.TrimSpace(ansiEscape.ReplaceAllString(line, ""))
	}

	line = p.stripTimestamp(obs, line)
	line = p.stripProcess(obs, line)

	// The unwrapped payload is what template mining should see, unless
	// nothing was unwrapped (Message stays empty, Raw is used).
	if line != strings.TrimSpace(obs.Log.Raw) {
		obs.Log.Message = line
	}

	if strings.HasPrefix(line, "{") {
		p.parseJSON(obs, line)
	}

	return true, nil
}

// stripTimestamp extracts a leading RFC3339 timestamp token.
func (p *ParseLog) stripTimestamp(obs *model.Observation, line string) string {
	token, rest, found := strings.Cut(line, " ")
	if !found {
		return line
	}

	timestamp, err := time.Parse(time.RFC3339Nano, token)
	if err != nil {
		return line
	}

	if obs.Timestamp.IsZero() {
		obs.Timestamp = timestamp
	}

	return strings.TrimSpace(rest)
}

// stripProcess moves a bracketed emitter prefix into the log.process
// attribute.
func (p *ParseLog) stripProcess(obs *model.Observation, line string) string {
	match := processPrefix.FindStringSubmatch(line)
	if match == nil {
		return line
	}

	if obs.Attributes == nil {
		obs.Attributes = map[string]string{}
	}

	obs.Attributes[AttrLogProcess] = match[1] + "[" + match[2] + "]"

	return line[len(match[0]):]
}

func (p *ParseLog) parseJSON(obs *model.Observation, line string) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		// Not actually JSON: mine the raw line.
		return
	}

	for _, key := range p.messageKeys {
		if message, ok := fields[key].(string); ok && message != "" {
			obs.Log.Message = strings.TrimSpace(message)
			break
		}
	}

	for _, key := range p.levelKeys {
		if level := normalizeLevel(fields[key]); level != "" {
			obs.Log.Level = level
			break
		}
	}

	if obs.Timestamp.IsZero() {
		for _, key := range p.timeKeys {
			if timestamp, ok := parseTimeValue(fields[key]); ok {
				obs.Timestamp = timestamp
				break
			}
		}
	}
}

func normalizeLevel(value any) string {
	level, ok := value.(string)
	if !ok {
		// A numeric level is a syslog priority: that is what RFC 5424
		// says, and every producer that emits a number follows it.
		return syslogLevel(value)
	}

	// A level quoted as a string is still a priority when it reads as
	// a number ("4"), and a word otherwise.
	if _, err := strconv.ParseInt(level, 10, 64); err == nil {
		return syslogLevel(level)
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

		// An epoch quoted as a string, which any producer may do to
		// keep the full precision of a 64-bit integer through JSON.
		if epoch, err := strconv.ParseFloat(v, 64); err == nil {
			return parseTimeValue(epoch)
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
