package prometheus

import (
	"testing"
	"time"
)

func TestParseLine(t *testing.T) {
	cases := []struct {
		line      string
		name      string
		value     float64
		labels    map[string]string
		timestamp time.Time
		skipped   bool
		invalid   bool
	}{
		{line: "", skipped: true},
		{line: "# HELP http_requests_total Total requests", skipped: true},
		{line: "http_requests_total 1027", name: "http_requests_total", value: 1027},
		{line: "http_requests_total 1027 1395066363000", name: "http_requests_total", value: 1027, timestamp: time.UnixMilli(1395066363000)},
		{
			line:   `http_requests_total{method="post",code="200"} 1027`,
			name:   "http_requests_total",
			value:  1027,
			labels: map[string]string{"method": "post", "code": "200"},
		},
		{
			line:   `msdos_file_access_time{path="C:\\DIR",error="Cannot find \"file\""} 1.458e+09`,
			name:   "msdos_file_access_time",
			value:  1.458e+09,
			labels: map[string]string{"path": `C:\DIR`, "error": `Cannot find "file"`},
		},
		{line: "no_value", invalid: true},
		{line: `broken{label="unterminated} 1`, invalid: true},
		{line: "http_requests_total abc", invalid: true},
	}

	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			sample, timestamp, err := ParseLine(tc.line)

			if tc.invalid {
				if err == nil {
					t.Fatalf("expected error, got sample %+v", sample)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %+v", err)
			}

			if tc.skipped {
				if sample != nil {
					t.Fatalf("expected skipped line, got sample %+v", sample)
				}

				return
			}

			if sample.Name != tc.name {
				t.Errorf("expected name %q, got %q", tc.name, sample.Name)
			}

			if sample.Value != tc.value {
				t.Errorf("expected value %v, got %v", tc.value, sample.Value)
			}

			if !timestamp.Equal(tc.timestamp) {
				t.Errorf("expected timestamp %v, got %v", tc.timestamp, timestamp)
			}

			if len(tc.labels) != len(sample.Labels) {
				t.Fatalf("expected labels %+v, got %+v", tc.labels, sample.Labels)
			}

			for k, v := range tc.labels {
				if sample.Labels[k] != v {
					t.Errorf("expected label %q = %q, got %q", k, v, sample.Labels[k])
				}
			}
		})
	}
}
