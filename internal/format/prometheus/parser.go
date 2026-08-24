package prometheus

import (
	"strconv"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

// ParseLine parses a single sample in the Prometheus text exposition
// format:
//
//	metric_name{label="value",...} 42.5 [timestamp_ms]
//
// Comments (# ...) and blank lines yield a nil sample without error. The
// returned time is zero when the line carries no timestamp.
func ParseLine(line string) (*model.MetricSample, time.Time, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil, time.Time{}, nil
	}

	name, rest, err := parseName(line)
	if err != nil {
		return nil, time.Time{}, errors.WithStack(err)
	}

	var labels map[string]string
	if strings.HasPrefix(rest, "{") {
		labels, rest, err = parseLabels(rest)
		if err != nil {
			return nil, time.Time{}, errors.WithStack(err)
		}
	}

	fields := strings.Fields(rest)
	if len(fields) < 1 || len(fields) > 2 {
		return nil, time.Time{}, errors.Errorf("malformed sample %q", line)
	}

	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, time.Time{}, errors.Wrapf(err, "malformed value %q", fields[0])
	}

	var timestamp time.Time
	if len(fields) == 2 {
		millis, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, time.Time{}, errors.Wrapf(err, "malformed timestamp %q", fields[1])
		}

		timestamp = time.UnixMilli(millis)
	}

	return &model.MetricSample{
		Name:   name,
		Value:  value,
		Labels: labels,
	}, timestamp, nil
}

func parseName(line string) (string, string, error) {
	end := strings.IndexFunc(line, func(r rune) bool {
		return !isNameRune(r)
	})

	if end == 0 {
		return "", "", errors.Errorf("malformed metric name in %q", line)
	}

	if end == -1 {
		return "", "", errors.Errorf("missing value in %q", line)
	}

	return line[:end], line[end:], nil
}

func isNameRune(r rune) bool {
	return r == '_' || r == ':' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}

func parseLabels(rest string) (map[string]string, string, error) {
	labels := map[string]string{}

	// Skip '{'.
	remaining := rest[1:]

	for {
		remaining = strings.TrimLeft(remaining, " \t")

		if strings.HasPrefix(remaining, "}") {
			return labels, remaining[1:], nil
		}

		end := strings.IndexFunc(remaining, func(r rune) bool {
			return !isNameRune(r)
		})
		if end <= 0 {
			return nil, "", errors.Errorf("malformed label name in %q", rest)
		}

		name := remaining[:end]
		remaining = remaining[end:]

		if !strings.HasPrefix(remaining, "=\"") {
			return nil, "", errors.Errorf("malformed label value in %q", rest)
		}

		value, next, err := parseQuoted(remaining[1:])
		if err != nil {
			return nil, "", errors.WithStack(err)
		}

		labels[name] = value
		remaining = strings.TrimPrefix(next, ",")
	}
}

func parseQuoted(quoted string) (string, string, error) {
	var b strings.Builder

	escaped := false
	for i := 1; i < len(quoted); i++ {
		c := quoted[i]

		if escaped {
			switch c {
			case 'n':
				b.WriteByte('\n')
			default:
				b.WriteByte(c)
			}

			escaped = false

			continue
		}

		switch c {
		case '\\':
			escaped = true
		case '"':
			return b.String(), quoted[i+1:], nil
		default:
			b.WriteByte(c)
		}
	}

	return "", "", errors.Errorf("unterminated label value in %q", quoted)
}
