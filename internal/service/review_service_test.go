package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
	"github.com/c-dvoid/code-review-agent-go/internal/provider"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type stubLLM struct {
	resp *domain.CompletionResponse
	err  error
}

func (s stubLLM) Name() string { return "stub" }
func (s stubLLM) Complete(context.Context, domain.CompletionRequest) (*domain.CompletionResponse, error) {
	return s.resp, s.err
}

// capturingLLM records the last request and answers with an empty approval.
type capturingLLM struct{ req domain.CompletionRequest }

func (c *capturingLLM) Name() string { return "capture" }
func (c *capturingLLM) Complete(_ context.Context, req domain.CompletionRequest) (*domain.CompletionResponse, error) {
	c.req = req
	return &domain.CompletionResponse{Text: `{"summary":"ok","verdict":"approve","issues":[]}`, StopReason: domain.StopReasonEndTurn}, nil
}

type stubSCM struct {
	diff     string
	files    []domain.ChangedFile
	contents map[string]string
	posted   string
	postErr  error
}

func (s *stubSCM) GetPullRequestDiff(context.Context, domain.PullRequestRef) (string, error) {
	return s.diff, nil
}
func (s *stubSCM) ListPullRequestFiles(context.Context, domain.PullRequestRef) ([]domain.ChangedFile, error) {
	return s.files, nil
}
func (s *stubSCM) GetFileContent(_ context.Context, _ domain.PullRequestRef, f domain.ChangedFile, maxBytes int) (string, error) {
	c, ok := s.contents[f.Path]
	if !ok {
		return "", domain.ErrNotFound
	}
	if len(c) > maxBytes {
		return "", domain.ErrInputTooLarge
	}
	return c, nil
}
func (s *stubSCM) PostReviewComment(_ context.Context, _ domain.PullRequestRef, body string) error {
	s.posted = body
	return s.postErr
}

func opts() Options { return Options{MaxInputBytes: 1000, MaxTokens: 1000, PostComments: true} }

func TestReview_MockProviderFlagsIssues(t *testing.T) {
	svc := NewReviewer(provider.NewMock(), nil, opts(), discard)
	report, err := svc.Review(context.Background(), domain.ReviewInput{
		Kind:    domain.InputKindCode,
		Content: "package main\n\n// TODO: fix\nvar password = \"hunter2\"\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != domain.VerdictRequestChanges {
		t.Errorf("verdict = %q, want request_changes", report.Verdict)
	}
	if len(report.Issues) != 2 {
		t.Fatalf("issues = %d, want 2: %+v", len(report.Issues), report.Issues)
	}
	if report.Issues[0].Severity != domain.SeverityCritical || report.Issues[0].Line != 4 {
		t.Errorf("first issue = %+v, want critical on line 4", report.Issues[0])
	}
}

func TestReview_Validation(t *testing.T) {
	svc := NewReviewer(provider.NewMock(), nil, opts(), discard)
	cases := map[string]struct {
		in   domain.ReviewInput
		want error
	}{
		"empty":     {domain.ReviewInput{Kind: domain.InputKindCode, Content: "  "}, domain.ErrInvalidInput},
		"bad kind":  {domain.ReviewInput{Kind: "x", Content: "a"}, domain.ErrInvalidInput},
		"too large": {domain.ReviewInput{Kind: domain.InputKindDiff, Content: string(make([]byte, 1001))}, domain.ErrInputTooLarge},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Review(context.Background(), tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestReview_ParsesFencedJSONAndTruncation(t *testing.T) {
	fenced := "Here you go:\n```json\n{\"summary\":\"ok\",\"verdict\":\"bogus\",\"issues\":[{\"severity\":\"MINOR\",\"title\":\"t\"}]}\n```"
	svc := NewReviewer(stubLLM{resp: &domain.CompletionResponse{Text: fenced, StopReason: domain.StopReasonEndTurn}}, nil, opts(), discard)
	report, err := svc.Review(context.Background(), domain.ReviewInput{Kind: domain.InputKindCode, Content: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != domain.VerdictComment || report.Issues[0].Severity != domain.SeverityMinor {
		t.Errorf("normalization failed: %+v", report)
	}

	svc = NewReviewer(stubLLM{resp: &domain.CompletionResponse{Text: "{", StopReason: domain.StopReasonMaxTokens}}, nil, opts(), discard)
	if _, err := svc.Review(context.Background(), domain.ReviewInput{Kind: domain.InputKindCode, Content: "x"}); !errors.Is(err, domain.ErrLLMBadResponse) {
		t.Fatalf("err = %v, want ErrLLMBadResponse", err)
	}
}

func TestReviewPullRequest_PostsCommentAndSurvivesPostFailure(t *testing.T) {
	scm := &stubSCM{diff: "+++ b/main.go\n+panic(\"boom\")\n"}
	svc := NewReviewer(provider.NewMock(), scm, opts(), discard)
	pr := domain.PullRequestRef{Owner: "o", Repo: "r", Number: 7}

	report, err := svc.ReviewPullRequest(context.Background(), pr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.CommentPosted || scm.posted == "" {
		t.Error("expected comment to be posted")
	}

	scm.postErr = errors.New("github down")
	report, err = svc.ReviewPullRequest(context.Background(), pr)
	if err != nil || report.CommentPosted {
		t.Fatalf("want report without comment, got report=%+v err=%v", report, err)
	}

	scm.postErr = fmt.Errorf("%w: head moved", domain.ErrStaleReview)
	report, err = svc.ReviewPullRequest(context.Background(), pr)
	if err != nil || report.CommentPosted {
		t.Fatalf("stale review: want report without comment, got report=%+v err=%v", report, err)
	}
}

func TestFormatMarkdown_ShowsReviewedCommit(t *testing.T) {
	r := &domain.ReviewReport{
		Verdict:     domain.VerdictApprove,
		PullRequest: &domain.PullRequestRef{HeadSHA: "0123456789abcdef"},
	}
	if md := FormatMarkdown(r); !strings.Contains(md, "commit `0123456`") {
		t.Errorf("footer missing short commit SHA:\n%s", md)
	}
	if md := FormatMarkdown(&domain.ReviewReport{}); strings.Contains(md, "commit `") {
		t.Errorf("footer shows a commit without a PR:\n%s", md)
	}
}

func TestReviewPullRequest_IncludesFileContext(t *testing.T) {
	scm := &stubSCM{
		diff: "+++ b/a.go\n+x := 1\n",
		files: []domain.ChangedFile{
			{Path: "a.go", Status: "modified"},
			{Path: "gone.go", Status: domain.FileStatusRemoved},
			{Path: "logo.png", Status: "added"},
			{Path: "big.go", Status: "added"},
			{Path: "missing.go", Status: "modified"},
		},
		contents: map[string]string{
			"a.go":     "package a\nfunc f() {\n\tx := 1\n}\n",
			"gone.go":  "package gone\n",
			"logo.png": "\x89PNG\x00\x00",
			"big.go":   strings.Repeat("x", 200),
		},
	}
	llm := &capturingLLM{}
	o := opts()
	o.ContextFiles, o.MaxContextFileBytes = true, 100
	svc := NewReviewer(llm, scm, o, discard)

	if _, err := svc.ReviewPullRequest(context.Background(), domain.PullRequestRef{Owner: "o", Repo: "r", Number: 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prompt := llm.req.UserPrompt
	for _, want := range []string{ContextOpenTag, "=== a.go ===\n1| package a\n2| func f() {\n3| \tx := 1\n4| }\n", ContextCloseTag} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"gone.go", "logo.png", "big.go", "missing.go"} {
		if strings.Contains(prompt, "=== "+unwanted) {
			t.Errorf("prompt should not include context for %s", unwanted)
		}
	}
	if strings.Index(prompt, ContextCloseTag) > strings.Index(prompt, ContentOpenTag) {
		t.Error("file context should come before the code under review")
	}
}

func TestReviewPullRequest_ContextRespectsInputBudget(t *testing.T) {
	scm := &stubSCM{
		diff:     "+++ b/a.go\n+x\n",
		files:    []domain.ChangedFile{{Path: "a.go"}, {Path: "b.go"}},
		contents: map[string]string{"a.go": strings.Repeat("a\n", 100), "b.go": strings.Repeat("b\n", 100)},
	}
	llm := &capturingLLM{}
	o := opts() // MaxInputBytes: 1000
	o.ContextFiles, o.MaxContextFileBytes = true, 1000
	svc := NewReviewer(llm, scm, o, discard)

	if _, err := svc.ReviewPullRequest(context.Background(), domain.PullRequestRef{Owner: "o", Repo: "r", Number: 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Each file is 200 bytes raw but ~590 once numbered, so only one fits in 1000.
	if !strings.Contains(llm.req.UserPrompt, "=== a.go") || strings.Contains(llm.req.UserPrompt, "=== b.go") {
		t.Errorf("want only a.go in context, got:\n%s", llm.req.UserPrompt)
	}

	o.ContextFiles = false
	svc = NewReviewer(llm, scm, o, discard)
	if _, err := svc.ReviewPullRequest(context.Background(), domain.PullRequestRef{Owner: "o", Repo: "r", Number: 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(llm.req.UserPrompt, ContextOpenTag) {
		t.Error("context should be omitted when ContextFiles is false")
	}
}
