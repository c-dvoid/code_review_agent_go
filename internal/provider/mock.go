package provider

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

// Mock is a deterministic, offline LLM provider for local development and
// tests. It flags a few well-known smells with simple string heuristics.
type Mock struct{}

var _ domain.LLMProvider = (*Mock)(nil)

// NewMock builds a mock provider.
func NewMock() *Mock { return &Mock{} }

// Name implements domain.LLMProvider.
func (m *Mock) Name() string { return "mock" }

type mockRule struct {
	needle   string
	severity domain.Severity
	category string
	title    string
	advice   string
}

var mockRules = []mockRule{
	{"password =", domain.SeverityCritical, "security", "Possible hard-coded credential", "Load secrets from the environment or a secret manager."},
	{"panic(", domain.SeverityMajor, "reliability", "Use of panic", "Return an error instead of panicking in library or request-path code."},
	{"_ = err", domain.SeverityMajor, "bug", "Ignored error", "Handle or propagate the error."},
	{"TODO", domain.SeverityInfo, "maintainability", "Unresolved TODO", "Track the TODO in an issue or resolve it before merging."},
	{"fmt.Println(", domain.SeverityMinor, "style", "Debug print statement", "Use the structured logger instead of fmt.Println."},
}

// Complete implements domain.LLMProvider.
func (m *Mock) Complete(ctx context.Context, req domain.CompletionRequest) (*domain.CompletionResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Only scan the submitted content so line numbers are relative to it.
	content := req.UserPrompt
	if _, after, ok := strings.Cut(content, "<code_to_review>\n"); ok {
		content, _, _ = strings.Cut(after, "</code_to_review>")
	}

	issues := []domain.Issue{}
	for i, line := range strings.Split(content, "\n") {
		for _, r := range mockRules {
			if strings.Contains(line, r.needle) {
				issues = append(issues, domain.Issue{
					Severity:    r.severity,
					Category:    r.category,
					Line:        i + 1,
					Title:       r.title,
					Description: "Found `" + r.needle + "` in: " + strings.TrimSpace(line),
					Suggestion:  r.advice,
				})
			}
		}
	}

	summary := "No issues detected by the mock reviewer."
	if len(issues) > 0 {
		summary = "The mock reviewer flagged potential issues based on simple heuristics."
	}
	out, _ := json.Marshal(map[string]any{
		"summary": summary,
		"verdict": "", // let the service derive it from the issues
		"issues":  issues,
	})

	return &domain.CompletionResponse{
		Text:       string(out),
		Model:      "mock-reviewer",
		StopReason: domain.StopReasonEndTurn,
		Usage: domain.TokenUsage{
			InputTokens:  int64(len(req.SystemPrompt)+len(req.UserPrompt)) / 4,
			OutputTokens: int64(len(out)) / 4,
		},
	}, nil
}
