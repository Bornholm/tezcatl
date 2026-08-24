package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
)

// EventSink POSTs every event as a JSON body to a webhook URL. It is
// meant to be wrapped in sink.Resilient so a slow or failing endpoint
// never blocks the pipeline.
type EventSink struct {
	url     string
	headers map[string]string
	client  *http.Client
}

func NewEventSink(url string, headers map[string]string) (*EventSink, error) {
	if url == "" {
		return nil, errors.New("missing webhook url")
	}

	return &EventSink{
		url:     url,
		headers: headers,
		client:  &http.Client{},
	}, nil
}

func (s *EventSink) Publish(ctx context.Context, events []model.Event) error {
	for _, evt := range events {
		body, err := json.Marshal(evt)
		if err != nil {
			return errors.WithStack(err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
		if err != nil {
			return errors.WithStack(err)
		}

		req.Header.Set("Content-Type", "application/json")
		for name, value := range s.headers {
			req.Header.Set(name, value)
		}

		res, err := s.client.Do(req)
		if err != nil {
			return errors.WithStack(err)
		}

		io.Copy(io.Discard, res.Body)
		res.Body.Close()

		if res.StatusCode >= 300 {
			return errors.Errorf("webhook returned status %d", res.StatusCode)
		}
	}

	return nil
}

func (s *EventSink) Close() error {
	return nil
}
