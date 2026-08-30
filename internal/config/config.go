package config

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/correlate"
	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/drain"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// Duration parses human-readable durations ("30s", "5m") from YAML.
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
	Server      Server      `yaml:"server"`
	Pipeline    Pipeline    `yaml:"pipeline"`
	Logs        Logs        `yaml:"logs"`
	Metrics     Metrics     `yaml:"metrics"`
	Plugins     Plugins     `yaml:"plugins"`
	Correlation Correlation `yaml:"correlation"`
	State       State       `yaml:"state"`
	Events      Events      `yaml:"events"`
	Sinks       Sinks       `yaml:"sinks"`
	Logging     Logging     `yaml:"logging"`
}

type Server struct {
	// Listen targets: unix:///run/tezcatl/tezcatl.sock, tcp://127.0.0.1:4242 or
	// tls://0.0.0.0:4243 (requires the tls section).
	Listen []string `yaml:"listen"`
	// TLS provides the certificate serving tls:// targets.
	TLS ServerTLS `yaml:"tls"`
}

type ServerTLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type Pipeline struct {
	Workers               int      `yaml:"workers"`
	ObservationBufferSize int      `yaml:"observation_buffer_size"`
	EventBufferSize       int      `yaml:"event_buffer_size"`
	FlushInterval         Duration `yaml:"flush_interval"`
	MaxLogLength          int      `yaml:"max_log_length"`
	// DebugEvents emits one debug.observation event per observation.
	DebugEvents bool `yaml:"debug_events"`
}

type Logs struct {
	Parsing   LogParsing   `yaml:"parsing"`
	Drain     drain.Config `yaml:"drain"`
	Detection LogDetection `yaml:"detection"`
}

type LogParsing struct {
	// Enabled turns structured parsing (JSON, timestamps, levels) on;
	// defaults to true.
	Enabled *bool `yaml:"enabled"`
	// MessageKeys, LevelKeys and TimeKeys are the JSON keys looked up
	// in a log envelope, in order, first match wins. Each defaults to
	// the names JSON loggers have in common; set one to name the keys
	// of a feed that uses its own (journald's MESSAGE, PRIORITY and
	// __REALTIME_TIMESTAMP, for instance). Replacing a list replaces
	// it whole, so repeat the defaults you want to keep.
	MessageKeys []string `yaml:"message_keys"`
	LevelKeys   []string `yaml:"level_keys"`
	TimeKeys    []string `yaml:"time_keys"`
}

type LogDetection struct {
	Enabled                   *bool                     `yaml:"enabled"`
	LearningPeriod            Duration                  `yaml:"learning_period"`
	RareThreshold             int64                     `yaml:"rare_threshold"`
	RareMinObservations       int64                     `yaml:"rare_min_observations"`
	SpikeBucket               Duration                  `yaml:"spike_bucket"`
	SpikeFactor               float64                   `yaml:"spike_factor"`
	SpikeMinCount             int64                     `yaml:"spike_min_count"`
	DisappearanceFactor       float64                   `yaml:"disappearance_factor"`
	DisappearanceMinCount     int64                     `yaml:"disappearance_min_count"`
	DisappearanceScanInterval Duration                  `yaml:"disappearance_scan_interval"`
	DisappearanceMaxCV        float64                   `yaml:"disappearance_max_cv"`
	Seasonality               string                    `yaml:"seasonality"`
	SeasonalMinObservations   int64                     `yaml:"seasonal_min_observations"`
	Markings                  map[string]detect.Marking `yaml:"markings"`
	MaxTemplates              int                       `yaml:"max_templates"`
}

type Metrics struct {
	Detection MetricDetection `yaml:"detection"`
}

// Plugins configures the ingestion source plugins (hashicorp/go-plugin
// subprocesses named tezcatl-source-<name>).
type Plugins struct {
	// Dir is where plugin binaries are installed; empty falls back to
	// $TEZCATL_PLUGINS_DIR then /usr/lib/tezcatl/plugins.
	Dir string `yaml:"dir"`
	// Sources maps a plugin name to its activation and configuration.
	Sources map[string]PluginSource `yaml:"sources"`
}

type PluginSource struct {
	Enabled bool `yaml:"enabled"`
	// Config is passed to the plugin as JSON; each plugin documents its
	// own schema.
	Config map[string]any `yaml:"config"`
}

type MetricDetection struct {
	Enabled        *bool                  `yaml:"enabled"`
	WarmupSamples  int64                  `yaml:"warmup_samples"`
	Alpha          float64                `yaml:"alpha"`
	ZThreshold     float64                `yaml:"z_threshold"`
	MinDeltas      map[string]float64     `yaml:"min_deltas"`
	TrendFastAlpha float64                `yaml:"trend_fast_alpha"`
	TrendSlowAlpha float64                `yaml:"trend_slow_alpha"`
	TrendThreshold float64                `yaml:"trend_threshold"`
	Thresholds     []detect.ThresholdRule `yaml:"thresholds"`
	MaxSeries      int                    `yaml:"max_series"`
}

type Correlation struct {
	Window        Duration `yaml:"window"`
	ContextBefore int      `yaml:"context_before"`
	ContextAfter  int      `yaml:"context_after"`
	// Clock is "wall" for live streams or "event" to expire windows on
	// observation timestamps (replaying past incidents).
	Clock string `yaml:"clock"`
	// ChangeHorizon is how far back changes are attached to events.
	ChangeHorizon Duration `yaml:"change_horizon"`
}

type State struct {
	// Dir is where learned state (templates, baselines) is persisted.
	// Empty disables persistence.
	Dir          string   `yaml:"dir"`
	SaveInterval Duration `yaml:"save_interval"`
}

// Events configures the local event log: the server's own queryable
// memory of what it published (tezcatl events, tezcatl top, the admin
// API), independent from the export sinks.
type Events struct {
	// Enabled defaults to true; the log still needs a directory, which
	// defaults to <state.dir>/events. With neither, nothing is kept.
	Enabled *bool  `yaml:"enabled"`
	Dir     string `yaml:"dir"`
	// Retention prunes whole days of events past this age; 0 keeps
	// everything.
	Retention Duration `yaml:"retention"`
}

// LogDir resolves the directory of the event log; empty means the log
// is off (disabled, or nowhere to write).
func (e *Events) LogDir(stateDir string) string {
	if e.Enabled != nil && !*e.Enabled {
		return ""
	}

	if e.Dir != "" {
		return e.Dir
	}

	if stateDir == "" {
		return ""
	}

	return filepath.Join(stateDir, "events")
}

type Sinks struct {
	Stdout   StdoutSink   `yaml:"stdout"`
	Postgres PostgresSink `yaml:"postgres"`
	Webhook  WebhookSink  `yaml:"webhook"`
}

type WebhookSink struct {
	Enabled bool `yaml:"enabled"`
	// URL receives one POST per event, with the event as JSON body.
	URL string `yaml:"url"`
	// Headers are added to every request (e.g. Authorization, with the
	// value coming from the environment).
	Headers     map[string]string `yaml:"headers"`
	QueueSize   int               `yaml:"queue_size"`
	MaxAttempts int               `yaml:"max_attempts"`
}

type StdoutSink struct {
	Enabled *bool `yaml:"enabled"`
}

type PostgresSink struct {
	Enabled bool `yaml:"enabled"`
	// DSN supports environment expansion, e.g. ${TEZCATL_POSTGRES_DSN}.
	DSN         string `yaml:"dsn"`
	QueueSize   int    `yaml:"queue_size"`
	MaxAttempts int    `yaml:"max_attempts"`
}

type Logging struct {
	// Level is one of debug, info, warn, error.
	Level string `yaml:"level"`
	// Format is one of text, json.
	Format string `yaml:"format"`
	// StatsInterval is how often internal pipeline counters are logged;
	// 0s disables periodic logging.
	StatsInterval Duration `yaml:"stats_interval"`
}

func Default() *Config {
	enabled := true

	return &Config{
		Server: Server{
			Listen: []string{"tcp://127.0.0.1:4242"},
		},
		Pipeline: Pipeline{
			ObservationBufferSize: 1024,
			EventBufferSize:       256,
			FlushInterval:         Duration(time.Second),
			MaxLogLength:          8192,
		},
		Logs: Logs{
			Drain: *drain.DefaultConfig(),
			Detection: LogDetection{
				Enabled:                   &enabled,
				LearningPeriod:            Duration(5 * time.Minute),
				RareThreshold:             3,
				RareMinObservations:       500,
				SpikeBucket:               Duration(time.Minute),
				SpikeFactor:               3,
				SpikeMinCount:             10,
				DisappearanceFactor:       3,
				DisappearanceMinCount:     10,
				DisappearanceScanInterval: Duration(30 * time.Second),
				DisappearanceMaxCV:        detect.DefaultDisappearanceMaxCV,
				Seasonality:               detect.SeasonalityHourly,
				SeasonalMinObservations:   50,
				MaxTemplates:              detect.DefaultMaxTemplates,
			},
		},
		Metrics: Metrics{
			Detection: MetricDetection{
				Enabled:        &enabled,
				WarmupSamples:  30,
				Alpha:          0.05,
				ZThreshold:     3,
				MinDeltas:      detect.DefaultMinDeltas(),
				TrendFastAlpha: 0.3,
				TrendSlowAlpha: 0.05,
				TrendThreshold: 0.5,
				MaxSeries:      detect.DefaultMaxSeries,
			},
		},
		Correlation: Correlation{
			Window:        Duration(30 * time.Second),
			ContextBefore: 10,
			ContextAfter:  10,
			Clock:         string(correlate.ClockWall),
			ChangeHorizon: Duration(15 * time.Minute),
		},
		State: State{
			SaveInterval: Duration(30 * time.Second),
		},
		Events: Events{
			// Two weeks of local memory by default: enough to look back
			// at an incident, small at the scale of an event stream that
			// is already deduplicated and correlated.
			Retention: Duration(15 * 24 * time.Hour),
		},
		Sinks: Sinks{
			Stdout: StdoutSink{Enabled: &enabled},
			Postgres: PostgresSink{
				QueueSize:   1024,
				MaxAttempts: 5,
			},
			Webhook: WebhookSink{
				QueueSize:   256,
				MaxAttempts: 5,
			},
		},
		Logging: Logging{
			Level:         "info",
			Format:        "text",
			StatsInterval: Duration(time.Minute),
		},
	}
}

// Load reads a YAML configuration file over the defaults, with strict
// key checking and ${VAR} environment expansion.
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
	if c.Pipeline.ObservationBufferSize <= 0 || c.Pipeline.EventBufferSize <= 0 {
		return errors.New("pipeline buffer sizes must be positive")
	}

	if c.Pipeline.Workers < 0 {
		return errors.New("pipeline.workers must not be negative")
	}

	if c.Correlation.Window <= 0 {
		return errors.New("correlation.window must be positive")
	}

	if c.Correlation.ContextBefore < 0 || c.Correlation.ContextAfter < 0 {
		return errors.New("correlation context sizes must not be negative")
	}

	switch correlate.Clock(c.Correlation.Clock) {
	case correlate.ClockWall, correlate.ClockEvent:
	default:
		return errors.Errorf("unsupported correlation.clock %q (expected wall or event)", c.Correlation.Clock)
	}

	switch c.Logs.Detection.Seasonality {
	case detect.SeasonalityNone, detect.SeasonalityHourly:
	default:
		return errors.Errorf("unsupported logs.detection.seasonality %q (expected none or hourly)", c.Logs.Detection.Seasonality)
	}

	for template, marking := range c.Logs.Detection.Markings {
		if !detect.ValidMarking(marking) {
			return errors.Errorf("unsupported marking %q for template %q", marking, template)
		}
	}

	if c.Sinks.Postgres.Enabled && c.Sinks.Postgres.DSN == "" {
		return errors.New("sinks.postgres.dsn is required when the postgres sink is enabled")
	}

	if c.Sinks.Webhook.Enabled && c.Sinks.Webhook.URL == "" {
		return errors.New("sinks.webhook.url is required when the webhook sink is enabled")
	}

	for _, target := range c.Server.Listen {
		if strings.HasPrefix(target, "tls://") && (c.Server.TLS.CertFile == "" || c.Server.TLS.KeyFile == "") {
			return errors.Errorf("listen target %q requires server.tls.cert_file and server.tls.key_file", target)
		}
	}

	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return errors.Errorf("unsupported logging.level %q", c.Logging.Level)
	}

	switch c.Logging.Format {
	case "text", "json":
	default:
		return errors.Errorf("unsupported logging.format %q", c.Logging.Format)
	}

	if _, err := drain.NewTemplateMiner(&c.Logs.Drain); err != nil {
		return errors.Wrap(err, "invalid logs.drain configuration")
	}

	for pattern := range c.Metrics.Detection.MinDeltas {
		if _, err := path.Match(pattern, "x"); err != nil {
			return errors.Wrapf(err, "invalid metrics.detection.min_deltas pattern %q", pattern)
		}
	}

	if c.Metrics.Detection.MaxSeries < 0 {
		return errors.New("metrics.detection.max_series cannot be negative (0 removes the cap)")
	}

	if c.Logs.Detection.MaxTemplates < 0 {
		return errors.New("logs.detection.max_templates cannot be negative (0 removes the cap)")
	}

	return nil
}

// LogDetectionConfig maps the configuration to the detector's own
// configuration type.
func (c *Config) LogDetectionConfig() *detect.LogConfig {
	detection := c.Logs.Detection

	return &detect.LogConfig{
		LearningPeriod:            detection.LearningPeriod.AsDuration(),
		RareThreshold:             detection.RareThreshold,
		RareMinObservations:       detection.RareMinObservations,
		SpikeBucket:               detection.SpikeBucket.AsDuration(),
		SpikeFactor:               detection.SpikeFactor,
		SpikeMinCount:             detection.SpikeMinCount,
		DisappearanceFactor:       detection.DisappearanceFactor,
		DisappearanceMinCount:     detection.DisappearanceMinCount,
		DisappearanceScanInterval: detection.DisappearanceScanInterval.AsDuration(),
		DisappearanceMaxCV:        detection.DisappearanceMaxCV,
		Seasonality:               detection.Seasonality,
		SeasonalMinObservations:   detection.SeasonalMinObservations,
		Markings:                  detection.Markings,
		MaxTemplates:              detection.MaxTemplates,
	}
}

func (c *Config) MetricDetectionConfig() *detect.MetricConfig {
	detection := c.Metrics.Detection

	return &detect.MetricConfig{
		WarmupSamples:  detection.WarmupSamples,
		Alpha:          detection.Alpha,
		ZThreshold:     detection.ZThreshold,
		MinDeltas:      detection.MinDeltas,
		TrendFastAlpha: detection.TrendFastAlpha,
		TrendSlowAlpha: detection.TrendSlowAlpha,
		TrendThreshold: detection.TrendThreshold,
		Thresholds:     detection.Thresholds,
		MaxSeries:      detection.MaxSeries,
	}
}

func (c *Config) CorrelationConfig() *correlate.Config {
	return &correlate.Config{
		Window:        c.Correlation.Window.AsDuration(),
		ContextBefore: c.Correlation.ContextBefore,
		ContextAfter:  c.Correlation.ContextAfter,
		Clock:         correlate.Clock(c.Correlation.Clock),
		ChangeHorizon: c.Correlation.ChangeHorizon.AsDuration(),
	}
}
