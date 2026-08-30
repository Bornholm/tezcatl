package journal

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"strconv"

	"github.com/pkg/errors"
)

// Options configure the journalctl invocation.
type Options struct {
	// Path is the journalctl binary; empty uses the one on PATH.
	Path string
	// Units restricts the read to these systemd units; empty reads
	// everything the caller may see.
	Units []string
	// Priority keeps entries at or below this syslog severity (0..7);
	// negative keeps everything.
	Priority int
	// Since is passed through as journalctl's --since; empty starts at
	// the tail, which is what a follower wants.
	Since string
	// Cursor resumes right after a previously recorded position and
	// wins over Since.
	Cursor string
	// User reads the user journal instead of the system one.
	User bool
	// Follow keeps the process running; tests turn it off.
	Follow bool
}

// Args builds the journalctl command line. It is separate from running
// it so the translation from configuration to flags can be tested
// without a journal.
func Args(opts Options) []string {
	args := []string{"--output=json", "--no-pager"}

	if opts.Follow {
		args = append(args, "--follow")
	}

	if opts.User {
		args = append(args, "--user")
	}

	for _, unit := range opts.Units {
		if unit != "" {
			args = append(args, "--unit="+unit)
		}
	}

	if opts.Priority >= 0 {
		args = append(args, "--priority="+strconv.Itoa(opts.Priority))
	}

	switch {
	case opts.Cursor != "":
		// Resuming beats any time window: it is exact, and it never
		// replays an entry already ingested.
		args = append(args, "--after-cursor="+opts.Cursor)
	case opts.Since != "":
		args = append(args, "--since="+opts.Since)
	default:
		// Without a position, start at the tail rather than replaying
		// the whole journal into the detectors.
		args = append(args, "--lines=0")
	}

	return args
}

// Reader streams entries from journalctl.
type Reader struct {
	opts Options
	// start is swapped in tests for a canned stream.
	start func(ctx context.Context, args []string) (io.ReadCloser, func() error, error)
}

func NewReader(opts Options) *Reader {
	r := &Reader{opts: opts}
	r.start = r.spawn

	return r
}

// SetStarter replaces the process spawning, for tests.
func SetStarter(r *Reader, start func(ctx context.Context, args []string) (io.ReadCloser, func() error, error)) {
	r.start = start
}

func (r *Reader) spawn(ctx context.Context, args []string) (io.ReadCloser, func() error, error) {
	path := r.opts.Path
	if path == "" {
		path = "journalctl"
	}

	cmd := exec.CommandContext(ctx, path, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, errors.Wrapf(err, "could not run %s", path)
	}

	return stdout, cmd.Wait, nil
}

// Read streams entries to handle until the journal ends or the context
// is cancelled. handle receives entries in journal order.
func (r *Reader) Read(ctx context.Context, handle func(Entry) error) error {
	stdout, wait, err := r.start(ctx, Args(r.opts))
	if err != nil {
		return errors.WithStack(err)
	}

	defer stdout.Close()

	scanner := bufio.NewScanner(stdout)
	// Journal entries hold whole stack traces; the default 64 KB limit
	// would end the stream on the first big one.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		entry, ok := Decode(scanner.Bytes())
		if !ok {
			// An entry without a usable message (a field-only record,
			// or binary content that decoded to nothing) carries
			// nothing to mine.
			continue
		}

		if err := handle(entry); err != nil {
			return errors.WithStack(err)
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return errors.WithStack(err)
	}

	if wait != nil {
		// journalctl exiting because the context ended is not a
		// failure.
		if err := wait(); err != nil && ctx.Err() == nil {
			return errors.Wrap(err, "journalctl stopped")
		}
	}

	return errors.WithStack(ctx.Err())
}
