package stdio

import (
	"bufio"
	"context"
	"io"
	"strings"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

// LogIngester reads newline-delimited log lines from a reader (typically
// a pipe on stdin) and emits one log observation per non-empty line.
//
// A blocked Read is only released by closing the underlying reader; the
// end of the pipe is therefore the primary termination signal, context
// cancellation being honored between reads.
type LogIngester struct {
	reader   io.Reader
	identity Identity
	now      func() time.Time
}

func NewLogIngester(reader io.Reader, identity Identity) *LogIngester {
	return &LogIngester{
		reader:   reader,
		identity: identity,
		now:      time.Now,
	}
}

func (i *LogIngester) Ingest(ctx context.Context, out chan<- model.Observation) error {
	scanner := bufio.NewScanner(i.reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		now := i.now()

		obs := model.Observation{
			ID:          model.NewID(),
			Service:     i.identity.Service,
			Environment: i.identity.Environment,
			Modality:    model.ModalityLog,
			IngestedAt:  now,
			Log: &model.LogRecord{
				Raw: line,
			},
		}

		select {
		case out <- obs:
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		}
	}

	if err := scanner.Err(); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
