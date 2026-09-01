// Package config holds the YAML configuration of the itztli web UI.
//
// Itztli is a pure client of a running tezcatl server: everything it
// shows comes from the AdminService, so the configuration is mostly
// "where is the server" and "who may look at it".
package config

import (
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// Duration parses human-readable durations ("30s", "12h") from YAML.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return errors.WithStack(err)
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return errors.Wrapf(err, "malformed duration %q", raw)
	}

	*d = Duration(parsed)

	return nil
}

func (d Duration) AsDuration() time.Duration {
	return time.Duration(d)
}

type Config struct {
	Server    Server    `yaml:"server"`
	Tezcatl   Tezcatl   `yaml:"tezcatl"`
	Auth      Auth      `yaml:"auth"`
	Incidents Incidents `yaml:"incidents"`
	GenAI     GenAI     `yaml:"genai"`
}

type Server struct {
	// Listen is a plain host:port; itztli speaks HTTP, TLS termination
	// belongs to the reverse proxy in front of it.
	Listen string `yaml:"listen"`
	// BaseURL is the public URL users reach itztli at. Required in
	// OIDC mode, where it anchors the callback URL.
	BaseURL string `yaml:"base_url"`
}

type Tezcatl struct {
	// Target is the tezcatl server to inspect: unix:///path,
	// tcp://host:port or tls://host:port.
	Target string `yaml:"target"`
	// TLSCA is a PEM CA bundle verifying a tls:// target (empty: the
	// system roots).
	TLSCA string `yaml:"tls_ca"`
}

type Auth struct {
	// Mode selects who may log in: "password" (the single local
	// account below) or "oidc".
	Mode       string   `yaml:"mode"`
	SessionTTL Duration `yaml:"session_ttl"`
	Password   Password `yaml:"password"`
	OIDC       OIDC     `yaml:"oidc"`
}

type Password struct {
	// Password is the single local account's password, usually
	// injected as ${ITZTLI_PASSWORD}. PasswordHash (bcrypt) wins over
	// it when both are set, and keeps the secret out of the file.
	Password     string `yaml:"password"`
	PasswordHash string `yaml:"password_hash"`
}

type OIDC struct {
	Issuer       string `yaml:"issuer"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	// Scopes always include openid; list extra ones here.
	Scopes []string `yaml:"scopes"`
	// ButtonLabel is the login button's text; empty derives one from
	// the issuer host.
	ButtonLabel string `yaml:"button_label"`
}

type Incidents struct {
	// Window is how far back the incident list looks. The server's own
	// event retention (360h by default) bounds it in practice.
	Window Duration `yaml:"window"`
	// DefaultRange preselects how far back the list looks. The reader
	// can widen it up to Window without reloading anything from the
	// server, since the whole window is fetched anyway.
	DefaultRange Duration `yaml:"default_range"`
	// DefaultSeverity preselects the minimum severity shown: critical,
	// warning, info, or all.
	DefaultSeverity string `yaml:"default_severity"`
	// Gap, MaxDuration and CoOccurrence tune the grouping, with the
	// same meaning as `tezcatl incidents`.
	Gap          Duration `yaml:"gap"`
	MaxDuration  Duration `yaml:"max_duration"`
	CoOccurrence Duration `yaml:"co_occurrence"`
	// CacheTTL is how long a fetched event list is reused before
	// asking the server again.
	CacheTTL Duration `yaml:"cache_ttl"`
	// PageSize is how many incidents a page shows before the
	// "load older" button.
	PageSize int `yaml:"page_size"`
}

// GenAI configures the LLM behind the Explain button. Leaving Provider
// empty disables the feature and hides the button.
type GenAI struct {
	// Provider is a genai chat-completion provider name: openai,
	// mistral or openrouter.
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	APIKey   string `yaml:"api_key"`
	// BaseURL overrides the provider's default endpoint
	// (OpenAI-compatible gateways, local models).
	BaseURL string `yaml:"base_url"`
	// SendLogContext carries the raw log lines around an incident to
	// the provider. They are what an explanation can be concrete
	// about; they are also production lines, unmasked, leaving the
	// machine. Unset means true.
	SendLogContext *bool `yaml:"send_log_context"`
}

// SendsLogContext reports whether the raw lines may leave the machine,
// defaulting to yes: an explanation without them can only restate what
// the reader already sees.
func (g GenAI) SendsLogContext() bool {
	return g.SendLogContext == nil || *g.SendLogContext
}

func Default() *Config {
	return &Config{
		Server: Server{
			Listen: "127.0.0.1:8484",
		},
		Tezcatl: Tezcatl{
			Target: "tcp://127.0.0.1:4242",
		},
		Auth: Auth{
			Mode:       "password",
			SessionTTL: Duration(12 * time.Hour),
		},
		Incidents: Incidents{
			Window: Duration(15 * 24 * time.Hour),
			// A dashboard opens on what needs an answer now, not on
			// two weeks of history: today, and only what the detectors
			// scored critical.
			DefaultRange:    Duration(24 * time.Hour),
			DefaultSeverity: "critical",
			CacheTTL:        Duration(10 * time.Second),
			PageSize:        20,
			Gap:             0, // zero values fall back to incident.Group's defaults
			MaxDuration:     0,
			CoOccurrence:    0,
		},
	}
}

func Load(path string) (*Config, error) {
	config := Default()

	if path == "" {
		return config, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	expanded := os.Expand(string(raw), func(name string) string {
		return os.Getenv(name)
	})

	decoder := yaml.NewDecoder(strings.NewReader(expanded))
	decoder.KnownFields(true)

	if err := decoder.Decode(config); err != nil {
		return nil, errors.Wrapf(err, "could not parse configuration %q", path)
	}

	if err := config.Validate(); err != nil {
		return nil, errors.Wrapf(err, "invalid configuration %q", path)
	}

	return config, nil
}

func (c *Config) Validate() error {
	if c.Server.Listen == "" {
		return errors.New("server.listen must not be empty")
	}

	if c.Tezcatl.Target == "" {
		return errors.New("tezcatl.target must not be empty")
	}

	switch c.Auth.Mode {
	case "password":
		if c.Auth.Password.Password == "" && c.Auth.Password.PasswordHash == "" {
			return errors.New("auth.password: set password (e.g. ${ITZTLI_PASSWORD}) or password_hash")
		}

	case "oidc":
		if c.Auth.OIDC.Issuer == "" || c.Auth.OIDC.ClientID == "" {
			return errors.New("auth.oidc: issuer and client_id are required")
		}

		if c.Server.BaseURL == "" {
			return errors.New("auth.oidc needs server.base_url (the OIDC callback is derived from it)")
		}

		if _, err := url.Parse(c.Server.BaseURL); err != nil {
			return errors.Wrap(err, "malformed server.base_url")
		}

	default:
		return errors.Errorf("unknown auth.mode %q (password, oidc)", c.Auth.Mode)
	}

	if c.Auth.SessionTTL.AsDuration() <= 0 {
		return errors.New("auth.session_ttl must be positive")
	}

	if c.Incidents.Window.AsDuration() <= 0 {
		return errors.New("incidents.window must be positive")
	}

	if c.Incidents.PageSize <= 0 {
		return errors.New("incidents.page_size must be positive")
	}

	if c.Incidents.DefaultRange.AsDuration() <= 0 {
		return errors.New("incidents.default_range must be positive")
	}

	if c.Incidents.DefaultRange.AsDuration() > c.Incidents.Window.AsDuration() {
		return errors.New("incidents.default_range must not exceed incidents.window")
	}

	switch c.Incidents.DefaultSeverity {
	case "critical", "warning", "info", "all":
	default:
		return errors.Errorf("unknown incidents.default_severity %q (critical, warning, info, all)", c.Incidents.DefaultSeverity)
	}

	if c.GenAI.Provider != "" && c.GenAI.Model == "" {
		return errors.New("genai.model is required when genai.provider is set")
	}

	return nil
}
