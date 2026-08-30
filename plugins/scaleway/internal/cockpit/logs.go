// Package cockpit reads the Scaleway Cockpit endpoints: Loki for logs,
// Prometheus for metrics. Both are plain HTTP APIs behind a token.
package cockpit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// Entry is one log line as Loki returns it, already unwrapped from the
// JSON envelope Scaleway puts around serverless container output.
type Entry struct {
	Timestamp time.Time
	// Raw is the line as stored, kept for the server.
	Raw string
	// Message is the payload extracted from the envelope, empty when
	// the line was not an envelope.
	Message string
	// Level is Loki's own detection, empty when it could not tell.
	Level string
	// ResourceID identifies the container; ResourceName is its
	// generated domain name.
	ResourceID   string
	ResourceName string
	// Instance is the ephemeral instance that produced the line. It is
	// an attribute, never an identity: a container that scales mints a
	// new one every time.
	Instance string
	// Stream is "stdout" or "stderr".
	Stream string
	Region string
}

// LogClient polls a Loki query_range endpoint. Loki has no follow mode,
// so the client keeps a cursor and asks for what happened since.
type LogClient struct {
	URL   string
	Token string
	HTTP  *http.Client

	// cursor is the timestamp of the newest entry already returned.
	cursor time.Time
	// seenAtCursor holds the identities of the entries already returned
	// at exactly the cursor timestamp: Loki bounds are inclusive, so
	// without this a line lands twice at every poll.
	seenAtCursor map[string]bool
}

func NewLogClient(endpoint string, token string, client *http.Client) *LogClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	return &LogClient{
		URL:          strings.TrimSuffix(endpoint, "/"),
		Token:        token,
		HTTP:         client,
		seenAtCursor: map[string]bool{},
	}
}

// Since positions the cursor before the first poll.
func (c *LogClient) Since(start time.Time) {
	c.cursor = start
	c.seenAtCursor = map[string]bool{}
}

type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// Poll returns the entries matching selector that are newer than the
// cursor, oldest first, and advances the cursor.
func (c *LogClient) Poll(ctx context.Context, selector string, limit int, now time.Time) ([]Entry, error) {
	if c.cursor.IsZero() {
		c.cursor = now.Add(-time.Minute)
	}

	if limit <= 0 {
		limit = 1000
	}

	query := url.Values{}
	query.Set("query", selector)
	query.Set("start", strconv.FormatInt(c.cursor.UnixNano(), 10))
	query.Set("end", strconv.FormatInt(now.UnixNano(), 10))
	query.Set("limit", strconv.Itoa(limit))
	// Oldest first, so a truncated page still advances the cursor
	// instead of leaving a hole behind.
	query.Set("direction", "forward")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/loki/api/v1/query_range?"+query.Encode(), nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	req.Header.Set("X-Token", c.Token)

	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, errors.Errorf("loki query_range returned %s", res.Status)
	}

	var decoded lokiResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return nil, errors.Wrap(err, "malformed loki response")
	}

	entries := []Entry{}

	for _, stream := range decoded.Data.Result {
		for _, value := range stream.Values {
			nanos, err := strconv.ParseInt(value[0], 10, 64)
			if err != nil {
				continue
			}

			timestamp := time.Unix(0, nanos)

			if timestamp.Before(c.cursor) {
				continue
			}

			identity := value[0] + "\x00" + value[1]
			if timestamp.Equal(c.cursor) && c.seenAtCursor[identity] {
				continue
			}

			entry := Entry{
				Timestamp:    timestamp,
				Raw:          value[1],
				ResourceID:   stream.Stream["resource_id"],
				ResourceName: stream.Stream["resource_name"],
				Region:       stream.Stream["region"],
			}

			if level := stream.Stream["detected_level"]; level != "" && level != "unknown" {
				entry.Level = level
			}

			if wrapped, ok := unwrap(value[1]); ok {
				if wrapped.Message == "" {
					// A blank line from the container: nothing to
					// mine, and keeping it would carry the instance
					// identifier into the templates.
					continue
				}

				entry.Message = wrapped.Message
				entry.Instance = wrapped.Instance
				entry.Stream = wrapped.Stream
			}

			entries = append(entries, entry)
		}
	}

	advance(c, entries)

	return sortByTime(entries), nil
}

// envelope is the JSON Scaleway wraps around each container line.
type envelope struct {
	Message  string `json:"message"`
	Instance string `json:"resource_instance"`
	Stream   string `json:"stream"`
}

// unwrap extracts the message, the emitting instance and the stream
// from the envelope. The boolean says whether the line was an envelope
// at all: one that is not is left to the server's own parsing, and one
// that is but carries no message is a blank line to be dropped. The
// distinction matters because the envelope holds the instance
// identifier, which changes at every deployment: mining the envelope of
// a blank line would mint a new template every time a container is
// redeployed.
func unwrap(line string) (envelope, bool) {
	if !strings.HasPrefix(strings.TrimSpace(line), "{") {
		return envelope{}, false
	}

	var decoded envelope
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		return envelope{}, false
	}

	if decoded.Instance == "" && decoded.Stream == "" {
		// JSON, but the application's own, not Scaleway's wrapper.
		return envelope{}, false
	}

	decoded.Message = strings.TrimSpace(decoded.Message)

	return decoded, true
}

// advance moves the cursor to the newest entry returned and remembers
// what was already delivered at that exact nanosecond.
func advance(c *LogClient, entries []Entry) {
	newest := c.cursor

	for _, entry := range entries {
		if entry.Timestamp.After(newest) {
			newest = entry.Timestamp
		}
	}

	if newest.After(c.cursor) {
		c.cursor = newest
		c.seenAtCursor = map[string]bool{}
	}

	for _, entry := range entries {
		if entry.Timestamp.Equal(c.cursor) {
			c.seenAtCursor[strconv.FormatInt(entry.Timestamp.UnixNano(), 10)+"\x00"+entry.Raw] = true
		}
	}
}

func sortByTime(entries []Entry) []Entry {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].Timestamp.Before(entries[j-1].Timestamp); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}

	return entries
}
