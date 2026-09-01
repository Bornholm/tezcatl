// Package explain turns one incident into a prose interpretation via
// an LLM. The interpretation is produced on demand and never stored:
// it is a reading of the report, not a record.
package explain

import (
	"context"
	"strings"
	"time"

	"github.com/bornholm/genai/llm"
	"github.com/bornholm/genai/llm/provider"
	"github.com/bornholm/genai/llm/provider/mistral"
	"github.com/bornholm/genai/llm/provider/openai"
	"github.com/bornholm/genai/llm/provider/openrouter"
	"github.com/bornholm/tezcatl/internal/core/incident"
	itzconfig "github.com/bornholm/tezcatl/internal/itztli/config"
	"github.com/pkg/errors"
)

type Explainer struct {
	client llm.ChatCompletionClient
	model  string
}

// New builds an explainer from the YAML genai section, or nil when the
// section is empty: no configuration, no Explain button.
func New(ctx context.Context, cfg itzconfig.GenAI) (*Explainer, error) {
	if cfg.Provider == "" {
		return nil, nil
	}

	option, err := chatCompletionOption(cfg)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	client, err := provider.Create(ctx, option)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return NewWithClient(client, cfg.Model), nil
}

// NewWithClient builds an explainer around an already-built chat
// completion client.
func NewWithClient(client llm.ChatCompletionClient, model string) *Explainer {
	return &Explainer{client: client, model: model}
}

// chatCompletionOption maps the YAML provider name to the provider's
// typed options. The factories use the options verbatim, so the
// provider's default endpoint is restated here for an empty base_url.
func chatCompletionOption(cfg itzconfig.GenAI) (provider.OptionFunc, error) {
	common := func(defaultBaseURL string) provider.CommonOptions {
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = defaultBaseURL
		}

		return provider.CommonOptions{
			Model:   cfg.Model,
			BaseURL: baseURL,
			APIKey:  cfg.APIKey,
		}
	}

	switch cfg.Provider {
	case string(openai.Name):
		return provider.WithChatCompletion(openai.Name, openai.Options{CommonOptions: common("https://api.openai.com/v1")}), nil
	case string(mistral.Name):
		return provider.WithChatCompletion(mistral.Name, mistral.Options{CommonOptions: common("https://api.mistral.ai/v1")}), nil
	case string(openrouter.Name):
		return provider.WithChatCompletion(openrouter.Name, openrouter.Options{CommonOptions: common("https://openrouter.ai/api/v1")}), nil
	default:
		return nil, errors.Errorf("unknown genai.provider %q (openai, mistral, openrouter)", cfg.Provider)
	}
}

// Model names the configured model, for the loading line of the UI.
func (e *Explainer) Model() string {
	return e.model
}

const systemPrompt = `Tu aides un administrateur système à lire un incident détecté par
tezcatl, un outil d'observabilité statistique. On te fournit le rapport
d'un seul incident, précédé de sa notice de lecture.

Réponds en français, en trois paragraphes courts au maximum, sans titre
ni liste. Explique ce que les données montrent, dans l'ordre : ce qui a
déclenché l'incident, ce qui l'accompagne, et ce qu'un déploiement
corrélé signifie ou non. Respecte strictement la notice : ne présente
jamais une corrélation comme une cause, ne devine pas les valeurs
masquées, et si les données n'identifient pas de cause, dis-le
plutôt que de choisir le candidat le plus plausible.`

// Explain sends the incident's self-describing markdown report to the
// model and returns its prose reading.
func (e *Explainer) Explain(ctx context.Context, entry incident.Incident) (string, error) {
	var report strings.Builder
	incident.RenderMarkdown(&report, []incident.Incident{entry}, incident.Period{Generated: time.Now()})

	res, err := e.client.ChatCompletion(ctx,
		llm.WithMessages(
			llm.NewMessage(llm.RoleSystem, systemPrompt),
			llm.NewMessage(llm.RoleUser, report.String()),
		),
	)
	if err != nil {
		return "", errors.WithStack(err)
	}

	text := strings.TrimSpace(res.Message().Content())
	if text == "" {
		return "", errors.New("the model returned an empty answer")
	}

	return text, nil
}
