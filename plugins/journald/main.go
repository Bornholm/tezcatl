// tezcatl-source-journald ingests the systemd journal.
//
// It reads `journalctl --output=json --follow` and turns each record
// into a log observation: the emitting unit names the service, the
// syslog priority becomes the level, and the entry keeps the timestamp
// the application logged with rather than the one it was ingested at.
//
// The plugin deliberately does not pre-parse the message text. Its job
// is to contribute what only it knows — priority, timestamp, identity —
// and to leave the server's generic parsing to do the rest, so an
// application logging JSON or ANSI colours inside the journal is still
// unwrapped normally.
//
// Configuration (JSON):
//
//	{
//	  "units": ["nginx.service"],  // empty reads everything visible
//	  "priority": 6,               // keep entries at or below this severity, -1 for all
//	  "since": "",                 // journalctl --since; empty starts at the tail
//	  "cursor_file": "",           // resume exactly where the last run stopped
//	  "user": false,               // read the user journal
//	  "environment": "production",
//	  "service": "",               // overrides the unit-derived identity
//	  "journalctl_path": "journalctl"
//	}
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	grpcadapter "github.com/bornholm/tezcatl/internal/adapter/grpc"
	"github.com/bornholm/tezcatl/internal/core/model"
	sdk "github.com/bornholm/tezcatl/pkg/plugin"
	"github.com/bornholm/tezcatl/plugins/journald/internal/journal"
	"github.com/pkg/errors"
)

type config struct {
	Units          []string `json:"units"`
	Priority       *int     `json:"priority"`
	Since          string   `json:"since"`
	CursorFile     string   `json:"cursor_file"`
	User           bool     `json:"user"`
	Environment    string   `json:"environment"`
	Service        string   `json:"service"`
	JournalctlPath string   `json:"journalctl_path"`
}

const defaultEnvironment = "production"

func main() {
	sdk.Serve(sdk.SourceFunc(stream))
}

func stream(ctx context.Context, rawConfig []byte, emit sdk.EmitFunc) error {
	cfg := config{}
	if len(rawConfig) > 0 {
		if err := json.Unmarshal(rawConfig, &cfg); err != nil {
			return errors.Wrap(err, "malformed plugin configuration")
		}
	}

	environment := cfg.Environment
	if environment == "" {
		environment = defaultEnvironment
	}

	// Keeping informational entries and above is a sane default: debug
	// is voluminous and rarely what an incident turns on.
	priority := 6
	if cfg.Priority != nil {
		priority = *cfg.Priority
	}

	opts := journal.Options{
		Path:     cfg.JournalctlPath,
		Units:    cfg.Units,
		Priority: priority,
		Since:    cfg.Since,
		Cursor:   readCursor(cfg.CursorFile),
		User:     cfg.User,
		Follow:   true,
	}

	if opts.Cursor != "" {
		slog.Info("resuming the journal from the recorded cursor")
	}

	reader := journal.NewReader(opts)

	cursors := newCursorWriter(cfg.CursorFile)

	cursorCtx, stopCursors := context.WithCancel(ctx)
	defer stopCursors()

	done := make(chan struct{})
	go func() {
		defer close(done)

		cursors.run(cursorCtx, time.Second)
	}()

	defer func() {
		stopCursors()
		<-done
	}()

	return errors.WithStack(reader.Read(ctx, func(entry journal.Entry) error {
		service := cfg.Service
		if service == "" {
			service = entry.Service()
		}

		if service == "" {
			service = "journal"
		}

		obs := model.Observation{
			Source:      environment + "/" + service,
			Service:     service,
			Environment: environment,
			Modality:    model.ModalityLog,
			Timestamp:   entry.Timestamp,
			Log: &model.LogRecord{
				// The message text is left raw on purpose: the server
				// still strips ANSI, unwraps a JSON payload and pulls a
				// leading timestamp out of it. Only the level, which no
				// generic rule could derive from the text, is filled
				// here from the syslog priority.
				Raw:   entry.Message,
				Level: entry.Level,
			},
			Attributes: attributes(entry),
		}

		if err := emit(grpcadapter.ToProtoObservation(&obs)); err != nil {
			return errors.WithStack(err)
		}

		cursors.record(entry.Cursor)

		return nil
	}))
}

func attributes(entry journal.Entry) map[string]string {
	attrs := map[string]string{}

	if entry.Unit != "" {
		attrs["journald.unit"] = entry.Unit
	}

	if entry.Identifier != "" && entry.Identifier != entry.Unit {
		attrs["journald.identifier"] = entry.Identifier
	}

	if entry.Hostname != "" {
		attrs["journald.hostname"] = entry.Hostname
	}

	if entry.PID != "" {
		attrs["journald.pid"] = entry.PID
	}

	if len(attrs) == 0 {
		return nil
	}

	return attrs
}

// cursorWriter records where the reader got to, so a restart resumes
// instead of replaying what it already ingested. Recording only marks
// the position; a ticker writes it at most once a second, because a log
// ingester must not turn every line into a disk write. The write has to
// be on a clock rather than on the records themselves: a burst of a
// thousand lines followed by an idle journal would otherwise leave the
// file holding the first line of the burst forever.
type cursorWriter struct {
	path string

	mu      sync.Mutex
	pending string
	written string
}

func newCursorWriter(path string) *cursorWriter {
	return &cursorWriter{path: path}
}

func (c *cursorWriter) record(cursor string) {
	if c.path == "" || cursor == "" {
		return
	}

	c.mu.Lock()
	c.pending = cursor
	c.mu.Unlock()
}

// run persists the position until the context ends, then once more so a
// clean shutdown loses nothing.
func (c *cursorWriter) run(ctx context.Context, interval time.Duration) {
	if c.path == "" {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.flush()

			return
		case <-ticker.C:
			c.flush()
		}
	}
}

func (c *cursorWriter) flush() {
	c.mu.Lock()
	pending, written := c.pending, c.written
	c.mu.Unlock()

	if c.path == "" || pending == "" || pending == written {
		return
	}

	if dir := filepath.Dir(c.path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			slog.Warn("could not create the cursor directory", slog.Any("error", err))

			return
		}
	}

	// Write then rename: a crash mid-write must not leave a truncated
	// cursor that resumes from nowhere.
	temporary := c.path + ".tmp"
	if err := os.WriteFile(temporary, []byte(pending), 0o600); err != nil {
		slog.Warn("could not write the journal cursor", slog.Any("error", err))

		return
	}

	if err := os.Rename(temporary, c.path); err != nil {
		slog.Warn("could not replace the journal cursor", slog.Any("error", err))

		return
	}

	c.mu.Lock()
	c.written = pending
	c.mu.Unlock()
}

func readCursor(path string) string {
	if path == "" {
		return ""
	}

	content, err := os.ReadFile(path)
	if err != nil {
		// No cursor yet, or an unreadable one: start from the
		// configured position rather than refusing to run.
		return ""
	}

	return strings.TrimSpace(string(content))
}
