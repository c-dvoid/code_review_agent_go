// Package service implements the review orchestration use cases.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

// Options tunes the review service.
type Options struct {
	MaxInputBytes int
	MaxTokens     int
	// PostComments posts PR reviews back to the source control provider.
	PostComments bool
	// ContextFiles sends the full content of changed files alongside a PR diff.
	ContextFiles bool
	// MaxContextFileBytes skips context files larger than this.
	MaxContextFileBytes int
}

// Reviewer implements domain.ReviewService.
type Reviewer struct {
	llm    domain.LLMProvider
	scm    domain.SourceControlProvider
	opts   Options
	logger *slog.Logger
	now    func() time.Time
}

var _ domain.ReviewService = (*Reviewer)(nil)

// NewReviewer wires a review service. scm may be nil if pull request reviews
// are not needed.
func NewReviewer(llm domain.LLMProvider, scm domain.SourceControlProvider, opts Options, logger *slog.Logger) *Reviewer {
	return &Reviewer{llm: llm, scm: scm, opts: opts, logger: logger, now: time.Now}
}

// Review implements domain.ReviewService.
func (s *Reviewer) Review(ctx context.Context, in domain.ReviewInput) (*domain.ReviewReport, error) {
	if err := s.validate(in); err != nil {
		return nil, err
	}

	started := s.now()
	resp, err := s.llm.Complete(ctx, domain.CompletionRequest{
		SystemPrompt:   systemPrompt,
		UserPrompt:     buildUserPrompt(in),
		MaxTokens:      s.opts.MaxTokens,
		ResponseSchema: reviewSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("llm completion: %w", err)
	}
	if resp.StopReason == domain.StopReasonMaxTokens {
		return nil, fmt.Errorf("%w: output hit the %d token limit; raise REVIEW_MAX_TOKENS or submit a smaller change",
			domain.ErrLLMBadResponse, s.opts.MaxTokens)
	}

	parsed, err := parseReview(resp.Text)
	if err != nil {
		return nil, err
	}

	report := &domain.ReviewReport{
		ID:          newID(),
		Summary:     parsed.Summary,
		Verdict:     domain.Verdict(parsed.Verdict),
		Issues:      parsed.Issues,
		Provider:    s.llm.Name(),
		Model:       resp.Model,
		Usage:       resp.Usage,
		DurationMS:  s.now().Sub(started).Milliseconds(),
		CreatedAt:   started.UTC(),
		PullRequest: in.PullRequest,
	}

	s.logger.InfoContext(ctx, "review completed",
		slog.String("review_id", report.ID),
		slog.String("provider", report.Provider),
		slog.String("model", report.Model),
		slog.String("verdict", string(report.Verdict)),
		slog.Int("issues", len(report.Issues)),
		slog.Int64("input_tokens", report.Usage.InputTokens),
		slog.Int64("output_tokens", report.Usage.OutputTokens),
		slog.Int64("duration_ms", report.DurationMS),
	)
	return report, nil
}

// ReviewPullRequest implements domain.ReviewService.
func (s *Reviewer) ReviewPullRequest(ctx context.Context, pr domain.PullRequestRef) (*domain.ReviewReport, error) {
	if s.scm == nil {
		return nil, fmt.Errorf("%w: no source control provider configured", domain.ErrInvalidInput)
	}
	if pr.Owner == "" || pr.Repo == "" || pr.Number <= 0 {
		return nil, fmt.Errorf("%w: pull request owner, repo and number are required", domain.ErrInvalidInput)
	}

	diff, err := s.scm.GetPullRequestDiff(ctx, pr)
	if err != nil {
		return nil, fmt.Errorf("fetch pull request diff: %w", err)
	}

	in := domain.ReviewInput{
		Kind:        domain.InputKindDiff,
		Content:     diff,
		PullRequest: &pr,
	}
	if s.opts.ContextFiles {
		in.Context = s.loadContext(ctx, pr, s.opts.MaxInputBytes-len(diff))
	}

	report, err := s.Review(ctx, in)
	if err != nil {
		return nil, err
	}

	if s.opts.PostComments {
		// A failed comment should not discard a successful (and paid-for) review.
		err := s.scm.PostReviewComment(ctx, pr, FormatMarkdown(report))
		switch {
		case errors.Is(err, domain.ErrStaleReview):
			// A newer push is (or will be) reviewed; its result belongs on the PR.
			s.logger.InfoContext(ctx, "skipped posting stale review comment",
				slog.String("review_id", report.ID),
				slog.String("pull_request", fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)),
				slog.Any("reason", err),
			)
		case err != nil:
			s.logger.ErrorContext(ctx, "failed to post review comment",
				slog.String("review_id", report.ID),
				slog.String("pull_request", fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)),
				slog.Any("error", err),
			)
		default:
			report.CommentPosted = true
		}
	}
	return report, nil
}

func (s *Reviewer) validate(in domain.ReviewInput) error {
	switch in.Kind {
	case domain.InputKindCode, domain.InputKindDiff:
	default:
		return fmt.Errorf("%w: unknown input kind %q", domain.ErrInvalidInput, in.Kind)
	}
	if strings.TrimSpace(in.Content) == "" {
		return fmt.Errorf("%w: content is empty", domain.ErrInvalidInput)
	}
	if len(in.Content) > s.opts.MaxInputBytes {
		return fmt.Errorf("%w: %d bytes (limit %d)", domain.ErrInputTooLarge, len(in.Content), s.opts.MaxInputBytes)
	}
	return nil
}

// loadContext fetches the full new version of each changed file, within
// budget bytes. Context only helps the model understand the diff, so it is
// best-effort: files that are removed, binary, too large or fail to load are
// skipped, and the review proceeds with whatever was collected.
func (s *Reviewer) loadContext(ctx context.Context, pr domain.PullRequestRef, budget int) []domain.ContextFile {
	prName := fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
	files, err := s.scm.ListPullRequestFiles(ctx, pr)
	if err != nil {
		s.logger.WarnContext(ctx, "could not list pull request files; reviewing diff only",
			slog.String("pull_request", prName), slog.Any("error", err))
		return nil
	}

	var out []domain.ContextFile
	var skipped []string
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		if f.Status == domain.FileStatusRemoved {
			continue
		}
		limit := min(s.opts.MaxContextFileBytes, budget)
		if limit <= 0 {
			skipped = append(skipped, f.Path)
			continue
		}
		content, err := s.scm.GetFileContent(ctx, pr, f, limit)
		if err != nil && !errors.Is(err, domain.ErrInputTooLarge) {
			s.logger.WarnContext(ctx, "could not load file context",
				slog.String("pull_request", prName), slog.String("file", f.Path), slog.Any("error", err))
		}
		// Count the numbered form, since that is what goes into the prompt.
		size := len(numberLines(content))
		if err != nil || !isText(content) || size > budget {
			skipped = append(skipped, f.Path)
			continue
		}
		budget -= size
		out = append(out, domain.ContextFile{Path: f.Path, Content: content})
	}

	s.logger.InfoContext(ctx, "loaded file context",
		slog.String("pull_request", prName),
		slog.Int("files", len(out)),
		slog.Int("skipped", len(skipped)),
		slog.Any("skipped_files", skipped),
	)
	return out
}

// isText reports whether content looks like source text rather than binary.
func isText(content string) bool {
	return utf8.ValidString(content) && !strings.ContainsRune(content, 0)
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "rev_" + hex.EncodeToString(b[:])
}
