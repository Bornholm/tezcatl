// Package humanize formats values for the summaries a person reads.
//
// The machine-readable side of an event stays untouched: signal
// attributes keep raw seconds, because an agent parsing "mean_interval_s"
// should not have to understand "27m 48s". Only the prose changes.
package humanize

import (
	"fmt"
	"strings"
	"time"
)

// Duration renders a duration the way someone would say it, keeping at
// most two units: "45s", "1m 3s", "27m 48s", "1h 40m", "2d 3h".
//
// "expected log template not seen for 6002s" tells a reader almost
// nothing; "not seen for 1h 40m" tells them their nightly job missed a
// run. Sub-minute durations keep one decimal below ten seconds, since
// the difference between 0.3s and 6s is the difference between a busy
// endpoint and a heartbeat.
func Duration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}

	sign := ""
	if d < 0 {
		sign, d = "-", -d
	}

	// Round to the smallest unit that will be shown, so 27m47.7s reads
	// as 27m 48s and 59m59.6s becomes 1h rather than 59m 60s.
	d = roundToScale(d)

	// Something too short to render is not nothing. A template arriving
	// every 40 milliseconds would read "0s", which looks like missing
	// data rather than a very busy stream.
	if d == 0 {
		return sign + "<0.1s"
	}

	switch {
	case d < 10*time.Second:
		// Below ten seconds the decimal carries meaning: 0.3s is a busy
		// endpoint, 6s is a heartbeat.
		text := strings.TrimSuffix(fmt.Sprintf("%.1f", d.Seconds()), ".0")

		return sign + text + "s"

	case d < time.Minute:
		return fmt.Sprintf("%s%ds", sign, int64(d/time.Second))

	case d < time.Hour:
		return sign + twoUnits(int64(d/time.Minute), "m", int64((d%time.Minute)/time.Second), "s")

	case d < 24*time.Hour:
		return sign + twoUnits(int64(d/time.Hour), "h", int64((d%time.Hour)/time.Minute), "m")

	default:
		return sign + twoUnits(int64(d/(24*time.Hour)), "d", int64((d%(24*time.Hour))/time.Hour), "h")
	}
}

// roundToScale rounds away the units the rendering will not show.
func roundToScale(d time.Duration) time.Duration {
	switch {
	case d < 10*time.Second:
		return d.Round(100 * time.Millisecond)
	case d < time.Hour:
		return d.Round(time.Second)
	case d < 24*time.Hour:
		return d.Round(time.Minute)
	default:
		return d.Round(time.Hour)
	}
}

// Seconds is Duration for a count of seconds, the form the detectors
// carry internally.
func Seconds(seconds float64) string {
	return Duration(time.Duration(seconds * float64(time.Second)))
}

// SignedDuration always shows the sign, for offsets read against a
// reference point: "+20s", "-3m".
func SignedDuration(d time.Duration) string {
	if d >= 0 {
		return "+" + Duration(d)
	}

	return Duration(d)
}

// twoUnits drops the smaller unit when it is zero, so an hour reads
// "1h" rather than "1h 0m".
func twoUnits(major int64, majorUnit string, minor int64, minorUnit string) string {
	if minor == 0 {
		return fmt.Sprintf("%d%s", major, majorUnit)
	}

	return fmt.Sprintf("%d%s %d%s", major, majorUnit, minor, minorUnit)
}
