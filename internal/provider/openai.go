package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

// OpenAICompatibleConfig configures any backend exposing the OpenAI
// Chat Completions API (OpenAI itself, Ollama, vLLM, LM Studio, ...).
type OpenAICompatibleConfig struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
	// HTTPClient is optional; a client with a sane timeout is used by default.
	HTTPClient *http.Client
}

// OpenAICompatible implements domain.LLMProvider over the Chat Completions API.
type OpenAICompatible struct {
	name    string
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

var _ domain.LLMProvider = (*OpenAICompatible)(nil)

// NewOpenAICompatible builds an OpenAI-compatible provider.
func NewOpenAICompatible(cfg OpenAICompatibleConfig) *OpenAICompatible {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	return &OpenAICompatible{
		name:    cfg.Name,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		http:    hc,
	}
}

// Name implements domain.LLMProvider.
func (o *OpenAICompatible) Name() string { return o.name }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model               string            `json:"model"`
	Messages            []chatMessage     `json:"messages"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	ResponseFormat      map[string]string `json:"response_format,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete implements domain.LLMProvider.
func (o *OpenAICompatible) Complete(ctx context.Context, req domain.CompletionRequest) (*domain.CompletionResponse, error) {
	body := chatRequest{
		Model: o.model,
		Messages: []chatMessage{
			{Role: "system", Content: req.SystemPrompt},
			{Role: "user", Content: req.UserPrompt},
		},
		MaxCompletionTokens: req.MaxTokens,
	}
	// json_object mode is the most widely supported structured mode across
	// OpenAI-compatible servers; the schema itself is described in the prompt.
	if req.ResponseSchema != nil {
		body.ResponseFormat = map[string]string{"type": "json_object"}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.http.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s: %v", domain.ErrProviderUnavailable, o.name, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: read body: %v", domain.ErrProviderUnavailable, o.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		sentinel := domain.ErrProviderFailure
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			sentinel = domain.ErrProviderUnavailable
		}
		return nil, fmt.Errorf("%w: %s status %d: %s", sentinel, o.name, resp.StatusCode, truncate(string(raw), 512))
	}

	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: %s: decode response: %v", domain.ErrLLMBadResponse, o.name, err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("%w: %s: no choices returned", domain.ErrLLMBadResponse, o.name)
	}

	choice := out.Choices[0]
	stop := domain.StopReasonEndTurn
	switch choice.FinishReason {
	case "length":
		stop = domain.StopReasonMaxTokens
	case "content_filter":
		return nil, fmt.Errorf("%w: %s content filter", domain.ErrLLMRefused, o.name)
	}

	return &domain.CompletionResponse{
		Text:       choice.Message.Content,
		Model:      out.Model,
		StopReason: stop,
		Usage: domain.TokenUsage{
			InputTokens:  out.Usage.PromptTokens,
			OutputTokens: out.Usage.CompletionTokens,
		},
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
