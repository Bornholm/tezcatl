package web

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
)

// Part is a fragment of a template or summary: either literal text or
// a placeholder mask, which the UI renders differently so nobody reads
// "<NUM>" as a value.
type Part struct {
	Text string
	Mask bool
}

var maskPattern = regexp.MustCompile(`<(?:NUM|IP|HEX|UUID|EMAIL|\*)>`)

// MaskParts splits a string on the drain masks so views can highlight
// them.
func MaskParts(s string) []Part {
	parts := []Part{}
	last := 0

	for _, loc := range maskPattern.FindAllStringIndex(s, -1) {
		if loc[0] > last {
			parts = append(parts, Part{Text: s[last:loc[0]]})
		}

		parts = append(parts, Part{Text: s[loc[0]:loc[1]], Mask: true})
		last = loc[1]
	}

	if last < len(s) {
		parts = append(parts, Part{Text: s[last:]})
	}

	return parts
}

// Severity carries the rendering of one severity level: its word, its
// glyph and the CSS class suffix.
type Severity struct {
	Word  string
	Glyph string
	Class string
}

func SeverityOf(severity model.Severity) Severity {
	switch severity {
	case model.SeverityCritical:
		return Severity{Word: "critical", Glyph: "■", Class: "crit"}
	case model.SeverityWarning:
		return Severity{Word: "warning", Glyph: "◆", Class: "warn"}
	default:
		return Severity{Word: string(severity), Glyph: "○", Class: "info"}
	}
}

// FrenchDuration writes a duration in full French words, the way the
// UI's prose does: "20 secondes", "59 minutes", "1 heure 40 min",
// "2 jours 3 h". At most two units, like core/humanize, but in words
// because it sits inside French sentences ("il y a…", "pendant…").
func FrenchDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}

	seconds := int64(d.Round(time.Second) / time.Second)

	plural := func(n int64, one string, many string) string {
		if n > 1 {
			return fmt.Sprintf("%d %s", n, many)
		}

		return fmt.Sprintf("%d %s", n, one)
	}

	switch {
	case seconds < 60:
		return plural(seconds, "seconde", "secondes")

	case seconds < 3600:
		minutes := (seconds + 30) / 60
		if minutes >= 60 {
			return "1 heure"
		}

		return plural(minutes, "minute", "minutes")

	case seconds < 86400:
		hours := seconds / 3600
		minutes := (seconds%3600 + 30) / 60
		if minutes >= 60 {
			hours++
			minutes = 0
		}

		text := plural(hours, "heure", "heures")
		if minutes > 0 {
			text += fmt.Sprintf(" %d min", minutes)
		}

		return text

	default:
		days := seconds / 86400
		hours := (seconds%86400 + 1800) / 3600
		if hours >= 24 {
			days++
			hours = 0
		}

		text := plural(days, "jour", "jours")
		if hours > 0 {
			text += fmt.Sprintf(" %d h", hours)
		}

		return text
	}
}

// Ago writes how long before now something happened.
func Ago(t time.Time, now time.Time) string {
	if since := now.Sub(t); since >= time.Second {
		return "il y a " + FrenchDuration(since)
	}

	return "à l'instant"
}

// FormatFloat writes a number the French way, with a precision suited
// to its magnitude: "13,74", "0,064", "2 840".
func FormatFloat(v float64) string {
	abs := v
	if abs < 0 {
		abs = -abs
	}

	var text string

	switch {
	case abs >= 100 || abs == float64(int64(abs)) && abs >= 10:
		text = groupThousands(strconv.FormatFloat(v, 'f', 0, 64))
	case abs >= 1 || abs == 0:
		text = trimZeros(strconv.FormatFloat(v, 'f', 2, 64))
	case abs >= 0.001:
		text = trimZeros(strconv.FormatFloat(v, 'f', 3, 64))
	default:
		text = strconv.FormatFloat(v, 'g', 2, 64)
	}

	return strings.ReplaceAll(text, ".", ",")
}

// FormatInt groups thousands with narrow no-break spaces: "11 423".
func FormatInt(n int64) string {
	return groupThousands(strconv.FormatInt(n, 10))
}

func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}

	s = strings.TrimRight(s, "0")

	return strings.TrimSuffix(s, ".")
}

func groupThousands(digits string) string {
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}

	if len(digits) <= 3 {
		return sign + digits
	}

	var groups []string
	for len(digits) > 3 {
		groups = append([]string{digits[len(digits)-3:]}, groups...)
		digits = digits[:len(digits)-3]
	}
	groups = append([]string{digits}, groups...)

	// U+202F, the narrow no-break space French typography groups
	// digits with.
	return sign + strings.Join(groups, " ")
}

var frenchMonths = [...]string{
	"janv.", "févr.", "mars", "avr.", "mai", "juin",
	"juil.", "août", "sept.", "oct.", "nov.", "déc.",
}

// AbsPeriod writes an incident's absolute time span in the local zone:
// "23:06:09 → 00:06:09 (UTC+02:00), 1 sept. 2026".
func AbsPeriod(start time.Time, end time.Time) string {
	start, end = start.Local(), end.Local()

	date := fmt.Sprintf("%d %s %d", start.Day(), frenchMonths[start.Month()-1], start.Year())
	if sy, sm, sd := start.Date(); true {
		if ey, em, ed := end.Date(); sy != ey || sm != em || sd != ed {
			date = fmt.Sprintf("%d %s %d → %d %s %d",
				sd, frenchMonths[sm-1], sy, ed, frenchMonths[em-1], ey)
		}
	}

	return fmt.Sprintf("%s → %s (UTC%s), %s",
		start.Format("15:04:05"), end.Format("15:04:05"), start.Format("-07:00"), date)
}

// Plural appends the label with a French plural s past one.
func Plural(n int, singular string) string {
	if n > 1 || n < -1 {
		return fmt.Sprintf("%d %ss", n, singular)
	}

	return fmt.Sprintf("%d %s", n, singular)
}
