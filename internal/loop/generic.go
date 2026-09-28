package loop

import (
	"context"
	"errors"
	"strings"

	"github.com/vectorphresh/0001-grodt/internal/openai"
)

// GenericProvider is the ordinary-prompt fallback. It reads Objective, Prompt, and Context
// and sets Response only after a successful synchronous LLM interaction.
type GenericProvider struct{ client openai.Client }

func NewGenericProvider(client openai.Client) *GenericProvider {
	return &GenericProvider{client: client}
}

func (p *GenericProvider) Handle(ctx context.Context, state *State) (bool, error) {
	if p == nil || p.client == nil {
		return false, errors.New("generic provider requires a client")
	}
	if ctx == nil {
		return false, errors.New("generic provider requires a context")
	}
	if state == nil || strings.TrimSpace(state.Prompt) == "" {
		return false, errors.New("generic provider requires a nonblank prompt")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	prompt := state.Prompt
	if len(state.Context) != 0 {
		prompt = "Context:\n" + strings.Join(state.Context, "\n\n") + "\n\nPrompt:\n" + state.Prompt
	}
	if state.Objective != "" {
		if len(state.Context) == 0 {
			prompt = "Prompt:\n" + prompt
		}
		prompt = "Objective:\n" + state.Objective + "\n\n" + prompt
	}
	response, err := p.client.Prompt(ctx, prompt)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	state.Response = response.Text
	return true, nil
}
