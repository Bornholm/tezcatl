package explain

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	"github.com/bornholm/tezcatl/internal/core/incident"
	"github.com/bornholm/tezcatl/internal/core/model"
)

// recorder keeps what was actually sent to the provider, which is the
// only thing that decides what an explanation can say.
type recorder struct {
	system string
	user   string
}

func (r *recorder) ChatCompletion(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	options := &llm.ChatCompletionOptions{}
	for _, apply := range funcs {
		apply(options)
	}

	for _, message := range options.Messages {
		switch message.Role() {
		case llm.RoleSystem:
			r.system = message.Content()
		case llm.RoleUser:
			r.user = message.Content()
		}
	}

	return llm.NewChatCompletionResponse(
		llm.NewMessage(llm.RoleAssistant, "une lecture"),
		llm.NewChatCompletionUsage(0, 0, 0),
	), nil
}

func sampleIncident() incident.Incident {
	at := time.Date(2026, 9, 1, 20, 2, 51, 0, time.UTC)

	trigger := model.Event{
		ID:        "ev-1",
		Kind:      "anomaly.log.frequency_spike",
		Source:    "production/blog",
		Service:   "blog",
		Timestamp: at,
		Severity:  model.SeverityCritical,
		Summary:   `log frequency spike: <IP> - - [<NUM>/Sep/<NUM>] "GET <*> HTTP/<NUM>.<NUM>" <NUM> <NUM>`,
		Signals: []model.Signal{{
			Kind:      "log.frequency_spike",
			Modality:  model.ModalityLog,
			Source:    "production/blog",
			Timestamp: at,
			Score:     0.91,
			Summary:   `log frequency spike: 10 occurrences in the current bucket, learned baseline 0.0`,
		}},
		Context: model.Context{
			Before: []model.Observation{{
				Modality:  model.ModalityLog,
				Timestamp: at.Add(-2 * time.Second),
				Log:       &model.LogRecord{Raw: `203.0.113.9 - - [01/Sep/2026:20:02:49 +0000] "GET /feed HTTP/1.1" 200 4192`},
			}},
			After: []model.Observation{{
				Modality:  model.ModalityLog,
				Timestamp: at.Add(time.Second),
				Log:       &model.LogRecord{Raw: `203.0.113.9 - - [01/Sep/2026:20:02:52 +0000] "GET /feed HTTP/1.1" 503 0`},
			}},
		},
	}

	// Built the way the UI builds it, so the evidence is derived
	// rather than invented.
	return incident.Group([]model.Event{trigger}, incident.Options{})[0]
}

// TestExplainSendsTheRawLines is the whole point of the feature: with
// only masked templates a model can say nothing concrete, and answers
// with methodology instead.
func TestExplainSendsTheRawLines(t *testing.T) {
	model := &recorder{}
	explainer := NewWithClient(model, "test-model")

	if _, err := explainer.Explain(context.Background(), sampleIncident()); err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{
		"203.0.113.9",
		"GET /feed HTTP/1.1",
		"503",
		"Raw log lines around the trigger",
	} {
		if !strings.Contains(model.user, expected) {
			t.Errorf("the report must carry %q", expected)
		}
	}
}

// TestExplainRedactsWhenAsked covers the operator who cannot let
// production lines reach a third party.
func TestExplainRedactsWhenAsked(t *testing.T) {
	model := &recorder{}
	explainer := NewWithClient(model, "test-model")
	explainer.sendLogContext = false

	entry := sampleIncident()

	if _, err := explainer.Explain(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(model.user, "203.0.113.9") {
		t.Error("no raw line may leave when the context is turned off")
	}

	if !strings.Contains(model.user, "frequency_spike") {
		t.Error("the incident itself must still be described")
	}

	// The redaction is on the copy sent out, not on what the UI shows.
	if len(entry.Trigger.Context.Before) == 0 {
		t.Error("redacting must not empty the caller's incident")
	}
}

// TestSystemPromptAsksForAnAnswerNotALecture guards the failure the
// first prompt produced: three paragraphs of epistemics and nothing an
// operator could act on.
func TestSystemPromptAsksForAnAnswerNotALecture(t *testing.T) {
	model := &recorder{}
	explainer := NewWithClient(model, "test-model")

	if _, err := explainer.Explain(context.Background(), sampleIncident()); err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{
		"lignes de log",
		"regarder ensuite",
	} {
		if !strings.Contains(model.system, expected) {
			t.Errorf("the prompt must ask for %q", expected)
		}
	}

	if !strings.Contains(model.system, "ne définis pas les types") {
		t.Error("the prompt must forbid restating the glossary")
	}
}
