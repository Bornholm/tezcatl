package setup

import (
	"log/slog"
	"os"

	"github.com/bornholm/tezcatl/internal/config"
)

// SetupLogging configures the default slog logger from the
// configuration. Internal logs go to stderr, keeping stdout for events.
func SetupLogging(cfg *config.Config) {
	level := slog.LevelInfo

	switch cfg.Logging.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if cfg.Logging.Format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	slog.SetDefault(slog.New(handler))
}
