// Package domain holds the core data models and the interfaces that decouple
// the review service from its external dependencies (LLMs, source control).
package domain

import "time"

// InputKind describes what kind of content is submitted for review.
type InputKind string

const (
	InputKindCode InputKind = "code"
	InputKindDiff InputKind = "diff"
)

// Severity ranks how serious a finding is.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityMajor    Severity = "major"
	SeverityMinor    Severity = "minor"
	SeverityInfo     Severity = "info"
)

// Rank returns a sortable weight; higher means more severe.
func (s Severity) Rank() int {
	switch s {
	case SeverityCritical:
		return 4
	case SeverityMajor:
		return 3
	case SeverityMinor:
		return 2
	default:
		return 1
	}
}

// Verdict is the overall recommendation of a review.
type Verdict string

const (
	VerdictApprove        Verdict = "approve"
	VerdictComment        Verdict = "comment"
	VerdictRequestChanges Verdict = "request_changes"
)

// ReviewInput is everything the review service needs to analyze a change.
type ReviewInput struct {
	Kind         InputKind
	Content      string
	Language     string
	Filename     string
	Instructions string
	// PullRequest is set when the input originates from a pull request.
	PullRequest *PullRequestRef
	// Context holds the full content of changed files so the model can see
	// the code around each diff hunk. It is optional and may be partial.
	Context []ContextFile
}

// ContextFile is the full content of a file in the new version of a change.
type ContextFile struct {
	Path    string
	Content string
}

// ChangedFile is a file touched by a pull request.
type ChangedFile struct {
	Path string
	// Status is the hosting provider's change status, e.g. "added", "modified" or "removed".
	Status string
	// BlobSHA identifies the file content in the new version.
	BlobSHA string
}

// FileStatusRemoved marks a file deleted by a pull request.
const FileStatusRemoved = "removed"

// PullRequestRef identifies a pull request in a source control system.
type PullRequestRef struct {
	Owner   string `json:"owner"`
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	Title   string `json:"title,omitempty"`
	Body    string `json:"body,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`
	HTMLURL string `json:"html_url,omitempty"`
}

// Issue is a single review finding.
type Issue struct {
	Severity    Severity `json:"severity"`
	Category    string   `json:"category"`
	File        string   `json:"file,omitempty"`
	Line        int      `json:"line,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Suggestion  string   `json:"suggestion,omitempty"`
}

// TokenUsage reports LLM token consumption for a single review.
type TokenUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// ReviewReport is the result of a code review.
type ReviewReport struct {
	ID          string          `json:"id"`
	Summary     string          `json:"summary"`
	Verdict     Verdict         `json:"verdict"`
	Issues      []Issue         `json:"issues"`
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	Usage       TokenUsage      `json:"usage"`
	DurationMS  int64           `json:"duration_ms"`
	CreatedAt   time.Time       `json:"created_at"`
	PullRequest *PullRequestRef `json:"pull_request,omitempty"`
	// CommentPosted reports whether the review was posted back to the pull request.
	CommentPosted bool `json:"comment_posted,omitempty"`
}
