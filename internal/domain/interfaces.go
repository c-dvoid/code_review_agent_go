package domain

import "context"

// CompletionRequest is a provider-agnostic LLM request.
type CompletionRequest struct {
	SystemPrompt string
	UserPrompt   string
	MaxTokens    int
	// ResponseSchema is an optional JSON Schema the response must conform to.
	// Providers that support structured output enforce it; others rely on the prompt.
	ResponseSchema map[string]any
}

// StopReason values normalized across providers.
const (
	StopReasonEndTurn   = "end_turn"
	StopReasonMaxTokens = "max_tokens"
	StopReasonRefusal   = "refusal"
)

// CompletionResponse is a provider-agnostic LLM response.
type CompletionResponse struct {
	Text       string
	Model      string
	StopReason string
	Usage      TokenUsage
}

// LLMProvider abstracts a large language model backend (Anthropic Claude,
// OpenAI, Ollama, a mock, ...). Implementations must honor ctx cancellation.
type LLMProvider interface {
	// Name returns a short identifier such as "anthropic" or "mock".
	Name() string
	// Complete sends a single-turn request and returns the model's text output.
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}

// SourceControlProvider abstracts a git hosting service (GitHub, GitLab, ...).
type SourceControlProvider interface {
	// GetPullRequestDiff returns the unified diff of a pull request.
	GetPullRequestDiff(ctx context.Context, pr PullRequestRef) (string, error)
	// ListPullRequestFiles returns the files changed by a pull request.
	ListPullRequestFiles(ctx context.Context, pr PullRequestRef) ([]ChangedFile, error)
	// GetFileContent returns the new-version content of a changed file. It
	// returns ErrInputTooLarge when the file is larger than maxBytes.
	GetFileContent(ctx context.Context, pr PullRequestRef, file ChangedFile, maxBytes int) (string, error)
	// PostReviewComment publishes a review as a comment on the pull request,
	// replacing the service's previous review comment there if one exists. It
	// returns ErrStaleReview when pr.HeadSHA is no longer the PR's head commit.
	PostReviewComment(ctx context.Context, pr PullRequestRef, body string) error
}

// ReviewService is the core use case: turning code into a review report.
type ReviewService interface {
	// Review analyzes raw code or a diff snippet.
	Review(ctx context.Context, in ReviewInput) (*ReviewReport, error)
	// ReviewPullRequest fetches a pull request diff, reviews it and, when
	// configured, posts the result back as a comment.
	ReviewPullRequest(ctx context.Context, pr PullRequestRef) (*ReviewReport, error)
}
