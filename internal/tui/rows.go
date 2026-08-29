package tui

import (
	"math"
	"sort"
	"strings"

	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/bornholm/tezcatl/internal/core/detect"
)

// templateRows filters and orders templates for display: grouped by
// partition, biggest clusters first so the dominant traffic is on top.
func templateRows(templates []admin.TemplateInfo, filter string) []admin.TemplateInfo {
	rows := make([]admin.TemplateInfo, 0, len(templates))

	for _, info := range templates {
		if !matchesFilter(filter, info.Partition, info.Template, string(info.Marking)) {
			continue
		}

		rows = append(rows, info)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Partition != rows[j].Partition {
			return rows[i].Partition < rows[j].Partition
		}

		return rows[i].Size > rows[j].Size
	})

	return rows
}

// metricRow pairs a series with the deviation score used for ordering.
type metricRow struct {
	detect.SeriesInfo

	// Deviation is |recent-mean| in standard deviations; +Inf when a
	// flat series moved, NaN during warmup.
	Deviation float64
}

// metricRows filters and orders series for display: the ones straying
// furthest from their baseline first, warming-up series last.
func metricRows(series []detect.SeriesInfo, filter string) []metricRow {
	rows := make([]metricRow, 0, len(series))

	for _, info := range series {
		if !matchesFilter(filter, info.Key) {
			continue
		}

		rows = append(rows, metricRow{SeriesInfo: info, Deviation: deviation(info)})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].Deviation, rows[j].Deviation

		if math.IsNaN(left) != math.IsNaN(right) {
			return math.IsNaN(right)
		}

		if left != right && !(math.IsNaN(left) && math.IsNaN(right)) {
			return left > right
		}

		return rows[i].Key < rows[j].Key
	})

	return rows
}

// deviation scores how far a series has strayed from its baseline.
func deviation(info detect.SeriesInfo) float64 {
	if info.Warmup {
		return math.NaN()
	}

	delta := math.Abs(info.Recent - info.Mean)

	if info.StdDev > 0 {
		return delta / info.StdDev
	}

	if delta == 0 {
		return 0
	}

	return math.Inf(1)
}

// matchesFilter reports whether any field contains the filter,
// case-insensitively; an empty filter matches everything.
func matchesFilter(filter string, fields ...string) bool {
	if filter == "" {
		return true
	}

	filter = strings.ToLower(filter)

	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), filter) {
			return true
		}
	}

	return false
}
