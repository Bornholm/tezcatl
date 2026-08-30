// Package journal decodes the JSON export format of journalctl.
package journal

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Entry is one journal record, reduced to what tezcatl needs.
type Entry struct {
	Timestamp time.Time
	Message   string
	// Level is the syslog priority translated to tezcatl's vocabulary.
	Level string
	// Unit, Identifier and Comm are the three ways journald names an
	// emitter, in decreasing order of precision.
	Unit       string
	Identifier string
	Comm       string
	Hostname   string
	PID        string
	// Cursor positions this entry in the journal; keeping the last one
	// lets a restarted reader resume exactly where it stopped.
	Cursor string
}

// Service names the emitter the way a person would: the unit without
// its type suffix, else the syslog identifier, else the command.
func (e Entry) Service() string {
	if e.Unit != "" {
		name := e.Unit

		for _, suffix := range []string{".service", ".socket", ".scope", ".mount", ".timer"} {
			if trimmed, found := strings.CutSuffix(name, suffix); found {
				name = trimmed

				break
			}
		}

		// A templated unit ("tezcatl-ingest@blog") names the instance,
		// which is what an operator recognizes.
		return name
	}

	if e.Identifier != "" {
		return e.Identifier
	}

	return e.Comm
}

// raw mirrors the journalctl JSON export. Every value is a string, or
// an array of byte values when the field is not valid UTF-8, or an
// array of strings when a field appeared several times in one entry.
type raw map[string]json.RawMessage

// Decode parses one JSON line of the export format.
func Decode(line []byte) (Entry, bool) {
	fields := raw{}
	if err := json.Unmarshal(line, &fields); err != nil {
		return Entry{}, false
	}

	entry := Entry{
		Message:    fields.text("MESSAGE"),
		Unit:       fields.text("_SYSTEMD_UNIT"),
		Identifier: fields.text("SYSLOG_IDENTIFIER"),
		Comm:       fields.text("_COMM"),
		Hostname:   fields.text("_HOSTNAME"),
		PID:        fields.text("_PID"),
		Cursor:     fields.text("__CURSOR"),
		Level:      priorityToLevel(fields.text("PRIORITY")),
	}

	// The application's own timestamp beats the journal's arrival time
	// when it is available.
	entry.Timestamp = microseconds(fields.text("_SOURCE_REALTIME_TIMESTAMP"))
	if entry.Timestamp.IsZero() {
		entry.Timestamp = microseconds(fields.text("__REALTIME_TIMESTAMP"))
	}

	if entry.Message == "" {
		return entry, false
	}

	return entry, true
}

// text reads a field whatever shape journald gave it.
func (f raw) text(key string) string {
	value, exists := f[key]
	if !exists {
		return ""
	}

	var asString string
	if err := json.Unmarshal(value, &asString); err == nil {
		return asString
	}

	// A field that is not valid UTF-8 is exported as an array of byte
	// values; a field repeated in one entry becomes an array of
	// strings. Both are arrays, so decide on the element type.
	var asBytes []int
	if err := json.Unmarshal(value, &asBytes); err == nil {
		octets := make([]byte, 0, len(asBytes))
		for _, b := range asBytes {
			if b < 0 || b > 255 {
				return ""
			}

			octets = append(octets, byte(b))
		}

		return strings.ToValidUTF8(string(octets), "")
	}

	var asStrings []string
	if err := json.Unmarshal(value, &asStrings); err == nil && len(asStrings) > 0 {
		return strings.Join(asStrings, " ")
	}

	return ""
}

// priorityToLevel maps an RFC 5424 severity to tezcatl's vocabulary.
func priorityToLevel(priority string) string {
	value, err := strconv.Atoi(strings.TrimSpace(priority))
	if err != nil {
		return ""
	}

	switch {
	case value <= 2:
		return "fatal"
	case value == 3:
		return "error"
	case value == 4:
		return "warn"
	case value <= 6:
		return "info"
	case value == 7:
		return "debug"
	default:
		return ""
	}
}

// microseconds reads the epoch microseconds journald exports as a
// string, so a 64-bit value survives JSON intact.
func microseconds(value string) time.Time {
	if value == "" {
		return time.Time{}
	}

	micros, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}
	}

	return time.UnixMicro(micros)
}
