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
	"github.com/bornholm/tezcatl/internal/core/model"
	itzconfig "github.com/bornholm/tezcatl/internal/itztli/config"
	"github.com/pkg/errors"
)

type Explainer struct {
	client llm.ChatCompletionClient
	model  string
	// sendLogContext carries the raw log lines to the provider. They
	// are what makes an explanation concrete, and they are also real
	// production lines leaving the machine: the choice belongs to
	// whoever runs itztli.
	sendLogContext bool
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

	explainer := NewWithClient(client, cfg.Model)
	explainer.sendLogContext = cfg.SendsLogContext()

	return explainer, nil
}

// NewWithClient builds an explainer around an already-built chat
// completion client.
func NewWithClient(client llm.ChatCompletionClient, model string) *Explainer {
	return &Explainer{client: client, model: model, sendLogContext: true}
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

const systemPrompt = `Tu écris pour l'administrateur système qui vient d'ouvrir cet
incident et qui se demande quoi faire. Il connaît déjà tezcatl : il n'a
pas besoin qu'on lui explique la méthode.

Réponds en français, en deux ou trois paragraphes courts, sans titre ni
liste, et dans cet ordre :

1. Ce qui s'est passé, concrètement. Appuie-toi sur les lignes de log
   brutes du rapport, avec leurs vraies valeurs (adresses, chemins,
   codes, messages) plutôt que sur les templates masqués. Cite-les
   quand elles portent l'information.
2. Ce qu'il vaut la peine de regarder ensuite, précisément : quel
   service, quelle machine, quelle fenêtre de temps, quelle commande ou
   quel tableau de bord.

N'écris pas ce que tezcatl ne sait pas faire, ne définis pas les types
de signaux, ne commente pas la façon dont les incidents sont regroupés
et ne paraphrase pas le rapport ligne à ligne. Ce sont des choses que
ton lecteur sait déjà, et elles prennent la place de ce qu'il ignore.

Deux réserves, en une clause chacune au maximum, et seulement si elles
changent ce qu'il faut faire : un déploiement proche est une
coïncidence dans le temps tant que rien ne l'a établi, et une valeur
masquée ne se devine pas. Si les données ne disent pas ce qui s'est
passé, écris-le en une phrase et passe tout de suite à ce qu'il faut
aller regarder.`

// Explain sends the incident's self-describing markdown report to the
// model and returns its prose reading.
func (e *Explainer) Explain(ctx context.Context, entry incident.Incident) (string, error) {
	if !e.sendLogContext {
		// Redact on the copy handed to the model; the incident the UI
		// shows keeps its lines.
		entry.Trigger.Context = model.Context{}
	}

	// No guidance sections: the system prompt above already says what
	// this reader must and must not do, and the glossary and limits
	// would outweigh the incident six to one.
	var report strings.Builder
	incident.RenderMarkdownWith(&report, []incident.Incident{entry},
		incident.Period{Generated: time.Now()}, incident.MarkdownOptions{})

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
