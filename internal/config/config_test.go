package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/detect"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	return path
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if cfg.Correlation.Window.AsDuration() != 30*time.Second {
		t.Fatalf("unexpected default correlation window: %v", cfg.Correlation.Window)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("default configuration must be valid: %+v", err)
	}
}

func TestLoadOverridesAndEnvExpansion(t *testing.T) {
	t.Setenv("TEZCATL_TEST_DSN", "postgres://user:secret@localhost:5432/tezcatl")

	path := writeConfig(t, `
server:
  listen:
    - unix:///run/tezcatl.sock
pipeline:
  workers: 4
logs:
  drain:
    sim_th: 0.5
    masking:
      - pattern: '\b\d+\b'
        mask_with: NUM
  detection:
    learning_period: 10m
    markings:
      "disk failure on <*>": symptomatic
correlation:
  window: 45s
sinks:
  postgres:
    enabled: true
    dsn: ${TEZCATL_TEST_DSN}
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if len(cfg.Server.Listen) != 1 || cfg.Server.Listen[0] != "unix:///run/tezcatl.sock" {
		t.Errorf("unexpected listen targets: %+v", cfg.Server.Listen)
	}

	if cfg.Pipeline.Workers != 4 {
		t.Errorf("unexpected workers: %d", cfg.Pipeline.Workers)
	}

	if cfg.Logs.Drain.SimTh != 0.5 {
		t.Errorf("unexpected sim_th: %f", cfg.Logs.Drain.SimTh)
	}

	if cfg.Logs.Detection.LearningPeriod.AsDuration() != 10*time.Minute {
		t.Errorf("unexpected learning period: %v", cfg.Logs.Detection.LearningPeriod)
	}

	if cfg.LogDetectionConfig().Markings["disk failure on <*>"] != detect.MarkingSymptomatic {
		t.Errorf("unexpected markings: %+v", cfg.Logs.Detection.Markings)
	}

	if cfg.Correlation.Window.AsDuration() != 45*time.Second {
		t.Errorf("unexpected correlation window: %v", cfg.Correlation.Window)
	}

	if cfg.Sinks.Postgres.DSN != "postgres://user:secret@localhost:5432/tezcatl" {
		t.Errorf("expected env expansion in dsn, got %q", cfg.Sinks.Postgres.DSN)
	}

	// Untouched defaults must survive a partial configuration.
	if cfg.Pipeline.ObservationBufferSize != 1024 {
		t.Errorf("unexpected observation buffer size: %d", cfg.Pipeline.ObservationBufferSize)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	path := writeConfig(t, `
pipelines:
  workers: 4
`)

	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown key to be rejected")
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := []string{
		"correlation:\n  window: -5s\n",
		"sinks:\n  postgres:\n    enabled: true\n",
		"logging:\n  level: verbose\n",
		"logs:\n  drain:\n    depth: 2\n",
		"logs:\n  drain:\n    masking:\n      - pattern: '['\n        mask_with: BAD\n",
	}

	for _, content := range cases {
		path := writeConfig(t, content)
		if _, err := Load(path); err == nil {
			t.Errorf("expected configuration to be rejected:\n%s", content)
		}
	}
}
