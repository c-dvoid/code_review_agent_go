package provider

import (
	"fmt"
	"strings"

	"github.com/c-dvoid/code-review-agent-go/internal/config"
	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

// New builds the LLM provider selected by LLM_PROVIDER.
func New(cfg *config.Config) (domain.LLMProvider, error) {
	switch strings.ToLower(cfg.LLM.Provider) {
	case "anthropic":
		return NewAnthropic(AnthropicConfig{
			APIKey:     cfg.Anthropic.APIKey,
			BaseURL:    cfg.Anthropic.BaseURL,
			Model:      cfg.Anthropic.Model,
			Effort:     cfg.Anthropic.Effort,
			Fallbacks:  cfg.Anthropic.Fallbacks,
			MaxRetries: cfg.Anthropic.MaxRetries,
		}), nil
	case "openai":
		return NewOpenAICompatible(OpenAICompatibleConfig{
			Name:    "openai",
			BaseURL: cfg.OpenAI.BaseURL,
			APIKey:  cfg.OpenAI.APIKey,
			Model:   cfg.OpenAI.Model,
		}), nil
	case "ollama":
		return NewOpenAICompatible(OpenAICompatibleConfig{
			Name:    "ollama",
			BaseURL: cfg.Ollama.BaseURL,
			Model:   cfg.Ollama.Model,
		}), nil
	case "mock":
		return NewMock(), nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider %q", cfg.LLM.Provider)
	}
}
