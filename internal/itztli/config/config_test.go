package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadExpandsEnvAndValidates(t *testing.T) {
	t.Setenv("ITZTLI_TEST_PASSWORD", "sesame")

	path := filepath.Join(t.TempDir(), "itztli.yaml")
	if err := os.WriteFile(path, []byte(`
server:
  listen: 127.0.0.1:9999
tezcatl:
  target: unix:///run/tezcatl/tezcatl.sock
auth:
  mode: password
  password:
    password: ${ITZTLI_TEST_PASSWORD}
incidents:
  window: 72h
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Auth.Password.Password != "sesame" {
		t.Errorf("env expansion failed: %q", cfg.Auth.Password.Password)
	}

	if cfg.Incidents.Window.AsDuration() != 72*time.Hour {
		t.Errorf("window = %s", cfg.Incidents.Window.AsDuration())
	}

	// Untouched keys keep their defaults.
	if cfg.Incidents.PageSize != 20 || cfg.Auth.SessionTTL.AsDuration() != 12*time.Hour {
		t.Error("defaults must survive a partial file")
	}
}

func TestValidateRejectsMisconfigurations(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"no credential":       func(c *Config) {},
		"oidc without issuer": func(c *Config) { c.Auth.Mode = "oidc" },
		"unknown mode":        func(c *Config) { c.Auth.Mode = "ldap" },
		"genai without model": func(c *Config) { c.Auth.Password.Password = "x"; c.GenAI.Provider = "mistral" },
		"oidc without base url": func(c *Config) {
			c.Auth.Mode = "oidc"
			c.Auth.OIDC.Issuer = "https://idp"
			c.Auth.OIDC.ClientID = "itztli"
		},
	} {
		cfg := Default()
		mutate(cfg)

		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}

	valid := Default()
	valid.Auth.Password.Password = "x"
	if err := valid.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}
