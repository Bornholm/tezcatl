package stdio

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

// JSONLSink writes events to a writer (typically stdout) as JSON Lines.
type JSONLSink struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func NewJSONLSink(writer io.Writer) *JSONLSink {
	return &JSONLSink{
		encoder: json.NewEncoder(writer),
	}
}

func (s *JSONLSink) Publish(ctx context.Context, events []model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, evt := range events {
		if err := s.encoder.Encode(evt); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

func (s *JSONLSink) Close() error {
	return nil
}
