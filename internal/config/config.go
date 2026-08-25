package config

import (
	"os"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/adapter/prometheus"
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
	Correlation Correlation `yaml:"correlation"`
	State       State       `yaml:"state"`
	Sinks       Sinks       `yaml:"sinks"`
	Logging     Logging     `yaml:"logging"`
}

type Server struct {
	// Listen targets: unix:///run/tezcatl.sock, tcp://127.0.0.1:4242 or
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
	Seasonality               string                    `yaml:"seasonality"`
	SeasonalMinObservations   int64                     `yaml:"seasonal_min_observations"`
	Markings                  map[string]detect.Marking `yaml:"markings"`
}

type Metrics struct {
	Detection  MetricDetection  `yaml:"detection"`
	Prometheus PrometheusSource `yaml:"prometheus"`
	System     SystemSource     `yaml:"system"`
	Docker     DockerSource     `yaml:"docker"`
}

type SystemSource struct {
	Enabled  bool     `yaml:"enabled"`
	Interval Duration `yaml:"interval"`
	// Service/Environment form the identity of the host metrics.
	Service     string `yaml:"service"`
	Environment string `yaml:"environment"`
	// DiskPaths are the mount points whose usage is reported.
	DiskPaths []string `yaml:"disk_paths"`
}

type DockerSource struct {
	Enabled  bool     `yaml:"enabled"`
	Interval Duration `yaml:"interval"`
	// Socket is the Docker Engine unix socket.
	Socket      string `yaml:"socket"`
	Environment string `yaml:"environment"`
	// ServiceLabel derives the service from a container label
	// (com.dokku.app-name by default); fallback: container name up to
	// the first dot.
	ServiceLabel string `yaml:"service_label"`
}

type PrometheusSource struct {
	Enabled bool `yaml:"enabled"`
	// URL is the Prometheus base URL, e.g. http://localhost:9090.
	URL      string   `yaml:"url"`
	Interval Duration `yaml:"interval"`
	// Service/Environment are the default identity of polled metrics.
	Service     string             `yaml:"service"`
	Environment string             `yaml:"environment"`
	Queries     []prometheus.Query `yaml:"queries"`
}

type MetricDetection struct {
	Enabled        *bool                  `yaml:"enabled"`
	WarmupSamples  int64                  `yaml:"warmup_samples"`
	Alpha          float64                `yaml:"alpha"`
	ZThreshold     float64                `yaml:"z_threshold"`
	TrendFastAlpha float64                `yaml:"trend_fast_alpha"`
	TrendSlowAlpha float64                `yaml:"trend_slow_alpha"`
	TrendThreshold float64                `yaml:"trend_threshold"`
	Thresholds     []detect.ThresholdRule `yaml:"thresholds"`
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
				Seasonality:               detect.SeasonalityHourly,
				SeasonalMinObservations:   50,
			},
		},
		Metrics: Metrics{
			Prometheus: PrometheusSource{
				Interval: Duration(30 * time.Second),
			},
			System: SystemSource{
				Interval:  Duration(30 * time.Second),
				Service:   "host",
				DiskPaths: []string{"/"},
			},
			Docker: DockerSource{
				Interval: Duration(30 * time.Second),
				Socket:   "/var/run/docker.sock",
			},
			Detection: MetricDetection{
				Enabled:        &enabled,
				WarmupSamples:  30,
				Alpha:          0.05,
				ZThreshold:     3,
				TrendFastAlpha: 0.3,
				TrendSlowAlpha: 0.05,
				TrendThreshold: 0.5,
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

	if c.Metrics.Prometheus.Enabled {
		if _, err := prometheus.NewPoller(c.PrometheusOptions()); err != nil {
			return errors.Wrap(err, "invalid metrics.prometheus configuration")
		}
	}

	return nil
}

func (c *Config) PrometheusOptions() *prometheus.Options {
	source := c.Metrics.Prometheus

	return &prometheus.Options{
		URL:         source.URL,
		Interval:    source.Interval.AsDuration(),
		Service:     source.Service,
		Environment: source.Environment,
		Queries:     source.Queries,
	}
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
		Seasonality:               detection.Seasonality,
		SeasonalMinObservations:   detection.SeasonalMinObservations,
		Markings:                  detection.Markings,
	}
}

func (c *Config) MetricDetectionConfig() *detect.MetricConfig {
	detection := c.Metrics.Detection

	return &detect.MetricConfig{
		WarmupSamples:  detection.WarmupSamples,
		Alpha:          detection.Alpha,
		ZThreshold:     detection.ZThreshold,
		TrendFastAlpha: detection.TrendFastAlpha,
		TrendSlowAlpha: detection.TrendSlowAlpha,
		TrendThreshold: detection.TrendThreshold,
		Thresholds:     detection.Thresholds,
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
