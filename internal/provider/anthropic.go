// Package provider contains implementations of domain.LLMProvider.
package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

const fallbackBeta = "server-side-fallback-2026-07-01"

// AnthropicConfig configures the Anthropic provider.
type AnthropicConfig struct {
	APIKey     string
	BaseURL    string
	Model      string
	Effort     string
	Fallbacks  bool
	MaxRetries int
}

// Anthropic implements domain.LLMProvider using the official Anthropic Go SDK.
type Anthropic struct {
	client    anthropic.Client
	model     string
	effort    string
	fallbacks bool
}

var _ domain.LLMProvider = (*Anthropic)(nil)

// NewAnthropic builds an Anthropic Claude provider.
func NewAnthropic(cfg AnthropicConfig) *Anthropic {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(cfg.MaxRetries),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Anthropic{
		client:    anthropic.NewClient(opts...),
		model:     cfg.Model,
		effort:    cfg.Effort,
		fallbacks: cfg.Fallbacks,
	}
}

// Name implements domain.LLMProvider.
func (a *Anthropic) Name() string { return "anthropic" }

// Complete implements domain.LLMProvider.
func (a *Anthropic) Complete(ctx context.Context, req domain.CompletionRequest) (*domain.CompletionResponse, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: int64(req.MaxTokens),
		System:    []anthropic.BetaTextBlockParam{{Text: req.SystemPrompt}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(req.UserPrompt)),
		},
	}

	// output_config carries the effort level and, when a schema is supplied,
	// structured-output enforcement so the reply is guaranteed to be valid JSON.
	outputConfig := map[string]any{"effort": a.effort}
	if req.ResponseSchema != nil {
		outputConfig["format"] = map[string]any{
			"type":   "json_schema",
			"schema": req.ResponseSchema,
		}
	}
	reqOpts := []option.RequestOption{option.WithJSONSet("output_config", outputConfig)}

	// Server-side fallbacks re-run a safety-classifier refusal on a model
	// Anthropic selects for the refusal category, inside the same call.
	if a.fallbacks {
		params.Betas = []anthropic.AnthropicBeta{fallbackBeta}
		reqOpts = append(reqOpts, option.WithJSONSet("fallbacks", "default"))
	}

	resp, err := a.client.Beta.Messages.New(ctx, params, reqOpts...)
	if err != nil {
		return nil, mapAnthropicError(err)
	}

	stop := string(resp.StopReason)
	if stop == domain.StopReasonRefusal {
		return nil, fmt.Errorf("%w: category=%q", domain.ErrLLMRefused, resp.StopDetails.Category)
	}

	// With fallbacks, a "fallback" block marks the boundary between a declined
	// attempt and its replacement; only text after the last boundary is the answer.
	var sb strings.Builder
	for _, block := range resp.Content {
		if block.Type == "fallback" {
			sb.Reset()
			continue
		}
		if tb, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}

	return &domain.CompletionResponse{
		Text:       sb.String(),
		Model:      string(resp.Model),
		StopReason: stop,
		Usage: domain.TokenUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}, nil
}

func mapAnthropicError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == 429 || apiErr.StatusCode == 529 || apiErr.StatusCode >= 500:
			return fmt.Errorf("%w: anthropic status %d: %v", domain.ErrProviderUnavailable, apiErr.StatusCode, err)
		default:
			return fmt.Errorf("%w: anthropic status %d: %v", domain.ErrProviderFailure, apiErr.StatusCode, err)
		}
	}
	return fmt.Errorf("%w: anthropic: %v", domain.ErrProviderUnavailable, err)
}
