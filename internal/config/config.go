// Package config loads service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
)

// Config is the root configuration of the service.
type Config struct {
	HTTPAddr        string        `env:"HTTP_ADDR" envDefault:":8080"`
	LogLevel        string        `env:"LOG_LEVEL" envDefault:"info"`
	RequestTimeout  time.Duration `env:"REQUEST_TIMEOUT" envDefault:"180s"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"30s"`
	MaxBodyBytes    int64         `env:"MAX_BODY_BYTES" envDefault:"2097152"`
	// APIKey protects the manual review endpoint when set (Authorization: Bearer <key>).
	APIKey string `env:"API_KEY"`

	Review    ReviewConfig
	LLM       LLMConfig
	Anthropic AnthropicConfig
	OpenAI    OpenAIConfig
	Ollama    OllamaConfig
	GitHub    GitHubConfig
	Webhook   WebhookConfig
}

// ReviewConfig tunes the review service.
type ReviewConfig struct {
	MaxInputBytes int `env:"REVIEW_MAX_INPUT_BYTES" envDefault:"400000"`
	MaxTokens     int `env:"REVIEW_MAX_TOKENS" envDefault:"16000"`
	// ContextFiles sends the full content of changed files alongside a PR diff.
	ContextFiles        bool `env:"REVIEW_CONTEXT_FILES" envDefault:"true"`
	MaxContextFileBytes int  `env:"REVIEW_MAX_CONTEXT_FILE_BYTES" envDefault:"100000"`
}

// LLMConfig selects the LLM backend.
type LLMConfig struct {
	// Provider is one of: anthropic, openai, ollama, mock.
	Provider string `env:"LLM_PROVIDER" envDefault:"anthropic"`
}

// AnthropicConfig configures the Anthropic Claude provider.
type AnthropicConfig struct {
	APIKey  string `env:"ANTHROPIC_API_KEY"`
	BaseURL string `env:"ANTHROPIC_BASE_URL"`
	Model   string `env:"ANTHROPIC_MODEL" envDefault:"claude-opus-5-5"`
	// Effort is one of: low, medium, high, xhigh, max.
	Effort string `env:"ANTHROPIC_EFFORT" envDefault:"high"`
	// Fallbacks enables server-side refusal fallbacks. Supported on
	// claude-opus-5-5, claude-opus-5, claude-fable-5-1 and claude-sonnet-5-5;
	// disable it for other models.
	Fallbacks  bool `env:"ANTHROPIC_FALLBACKS" envDefault:"true"`
	MaxRetries int  `env:"ANTHROPIC_MAX_RETRIES" envDefault:"2"`
}

// OpenAIConfig configures the OpenAI (or any OpenAI-compatible) provider.
type OpenAIConfig struct {
	APIKey  string `env:"OPENAI_API_KEY"`
	BaseURL string `env:"OPENAI_BASE_URL" envDefault:"https://api.openai.com/v1"`
	Model   string `env:"OPENAI_MODEL"`
}

// OllamaConfig configures a local Ollama server via its OpenAI-compatible API.
type OllamaConfig struct {
	BaseURL string `env:"OLLAMA_BASE_URL" envDefault:"http://localhost:11434/v1"`
	Model   string `env:"OLLAMA_MODEL"`
}

// GitHubConfig configures GitHub access and webhook verification.
type GitHubConfig struct {
	Token         string `env:"GITHUB_TOKEN"`
	WebhookSecret string `env:"GITHUB_WEBHOOK_SECRET"`
	APIBaseURL    string `env:"GITHUB_API_BASE_URL" envDefault:"https://api.github.com"`
	// PostComments posts reviews back to pull requests (requires Token).
	PostComments bool `env:"GITHUB_POST_COMMENTS" envDefault:"true"`
}

// WebhookConfig controls how webhook-triggered reviews are executed.
type WebhookConfig struct {
	// Async acknowledges webhooks immediately (202) and reviews in the background.
	// GitHub expects a response within 10 seconds, so keep this on in production.
	Async          bool          `env:"WEBHOOK_ASYNC" envDefault:"true"`
	MaxConcurrency int           `env:"WEBHOOK_MAX_CONCURRENCY" envDefault:"4"`
	MaxQueue       int           `env:"WEBHOOK_MAX_QUEUE" envDefault:"100"`
	JobTimeout     time.Duration `env:"WEBHOOK_JOB_TIMEOUT" envDefault:"10m"`
}

// Load parses environment variables into a validated Config.
// A .env file in the working directory is loaded first if present; variables
// already set in the real environment take precedence over it.
func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("load .env file: %w", err)
	}

	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("parse environment: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks cross-field requirements.
func (c *Config) Validate() error {
	var errs []error

	switch strings.ToLower(c.LLM.Provider) {
	case "anthropic":
		if c.Anthropic.APIKey == "" {
			errs = append(errs, errors.New("ANTHROPIC_API_KEY is required when LLM_PROVIDER=anthropic"))
		}
		switch c.Anthropic.Effort {
		case "low", "medium", "high", "xhigh", "max":
		default:
			errs = append(errs, fmt.Errorf("ANTHROPIC_EFFORT must be one of low|medium|high|xhigh|max, got %q", c.Anthropic.Effort))
		}
	case "openai":
		if c.OpenAI.APIKey == "" {
			errs = append(errs, errors.New("OPENAI_API_KEY is required when LLM_PROVIDER=openai"))
		}
		if c.OpenAI.Model == "" {
			errs = append(errs, errors.New("OPENAI_MODEL is required when LLM_PROVIDER=openai"))
		}
	case "ollama":
		if c.Ollama.Model == "" {
			errs = append(errs, errors.New("OLLAMA_MODEL is required when LLM_PROVIDER=ollama"))
		}
	case "mock":
	default:
		errs = append(errs, fmt.Errorf("unsupported LLM_PROVIDER %q (expected anthropic|openai|ollama|mock)", c.LLM.Provider))
	}

	if c.Review.MaxInputBytes <= 0 {
		errs = append(errs, errors.New("REVIEW_MAX_INPUT_BYTES must be positive"))
	}
	if c.Review.MaxTokens <= 0 {
		errs = append(errs, errors.New("REVIEW_MAX_TOKENS must be positive"))
	}
	if c.Review.ContextFiles && c.Review.MaxContextFileBytes <= 0 {
		errs = append(errs, errors.New("REVIEW_MAX_CONTEXT_FILE_BYTES must be positive"))
	}
	if c.Webhook.MaxConcurrency <= 0 {
		errs = append(errs, errors.New("WEBHOOK_MAX_CONCURRENCY must be positive"))
	}
	if _, err := c.SlogLevel(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// SlogLevel converts LOG_LEVEL into a slog.Level.
func (c *Config) SlogLevel() (slog.Level, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.LogLevel)); err != nil {
		return 0, fmt.Errorf("invalid LOG_LEVEL %q: %w", c.LogLevel, err)
	}
	return lvl, nil
}
