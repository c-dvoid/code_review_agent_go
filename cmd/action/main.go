// Command action runs a single pull request review inside GitHub Actions.
//
// It reads the pull_request event GitHub provides, reviews the pull request
// with the same service the HTTP server uses, posts the review comment, and
// writes a job summary. Action inputs arrive as INPUT_* environment variables
// and are mapped onto the server's configuration variables.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/c-dvoid/code-review-agent-go/internal/config"
	"github.com/c-dvoid/code-review-agent-go/internal/domain"
	"github.com/c-dvoid/code-review-agent-go/internal/provider"
	"github.com/c-dvoid/code-review-agent-go/internal/scm"
	"github.com/c-dvoid/code-review-agent-go/internal/service"
)

// actionsBotLogin authors comments made with an Actions run's GITHUB_TOKEN.
const actionsBotLogin = "github-actions[bot]"

// errFindings marks a review that found issues at or above the fail_on level.
var errFindings = errors.New("review found issues at or above the fail_on level")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Stdout); err != nil {
		// A workflow command makes the error show up as an annotation.
		fmt.Fprintf(os.Stdout, "::error::%s\n", escapeCommand(err.Error()))
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	logger := slog.New(slog.NewTextHandler(out, nil))

	if name := os.Getenv("GITHUB_EVENT_NAME"); name != "pull_request" && name != "pull_request_target" {
		notice(out, "Skipping: the %q event is not a pull request. Trigger this action with on: pull_request.", name)
		return nil
	}

	provName := strings.ToLower(input("provider", "anthropic"))
	if input("api_key", "") == "" && provName != "ollama" && provName != "mock" {
		// Workflows triggered from forks do not receive repository secrets.
		notice(out, "Skipping: no api_key. Pull requests from forks do not receive repository secrets.")
		return nil
	}
	if err := applyInputs(provName); err != nil {
		return err
	}
	threshold, err := failThreshold(input("fail_on", "none"))
	if err != nil {
		return err
	}

	body, err := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return fmt.Errorf("read event payload: %w", err)
	}
	evt, err := scm.ParsePullRequestEvent(body)
	if err != nil {
		return err
	}
	if reason := evt.SkipReason(); reason != "" {
		notice(out, "Skipping: %s.", reason)
		return nil
	}
	pr, err := evt.Ref()
	if err != nil {
		return err
	}

	// Never read a .env file here: the workspace may be a checkout of the
	// pull request, and its .env could redirect the API key elsewhere.
	cfg, err := config.LoadEnv()
	if err != nil {
		return err
	}
	llm, err := provider.New(cfg)
	if err != nil {
		return err
	}
	github := scm.NewGitHub(scm.GitHubConfig{
		BaseURL:       cfg.GitHub.APIBaseURL,
		Token:         cfg.GitHub.Token,
		MaxDiffBytes:  cfg.Review.MaxInputBytes,
		FallbackLogin: actionsBotLogin,
	})
	reviewer := service.NewReviewer(llm, github, service.Options{
		MaxInputBytes:       cfg.Review.MaxInputBytes,
		MaxTokens:           cfg.Review.MaxTokens,
		PostComments:        cfg.GitHub.PostComments && cfg.GitHub.Token != "",
		ContextFiles:        cfg.Review.ContextFiles,
		MaxContextFileBytes: cfg.Review.MaxContextFileBytes,
	}, logger)

	report, err := reviewer.ReviewPullRequest(ctx, pr)
	if err != nil {
		return fmt.Errorf("review %s/%s#%d: %w", pr.Owner, pr.Repo, pr.Number, err)
	}

	markdown := service.FormatMarkdown(report)
	if err := appendFile(os.Getenv("GITHUB_STEP_SUMMARY"), markdown); err != nil {
		logger.Warn("could not write job summary", slog.Any("error", err))
	}
	if err := appendFile(os.Getenv("GITHUB_OUTPUT"), fmt.Sprintf("verdict=%s\nissues=%d\nreview_id=%s\n", report.Verdict, len(report.Issues), report.ID)); err != nil {
		logger.Warn("could not write step outputs", slog.Any("error", err))
	}
	fmt.Fprint(out, markdown)

	if worst := worstRank(report.Issues); threshold > 0 && worst >= threshold {
		return errFindings
	}
	return nil
}

// input returns the action input name, or def when it is unset or blank.
func input(name, def string) string {
	if v := strings.TrimSpace(os.Getenv("INPUT_" + strings.ToUpper(name))); v != "" {
		return v
	}
	return def
}

// applyInputs maps action inputs onto the configuration variables read by
// config.LoadEnv. Blank inputs leave the variables (and their defaults) alone.
func applyInputs(prov string) error {
	prefix := map[string]string{"anthropic": "ANTHROPIC", "openai": "OPENAI", "ollama": "OLLAMA", "mock": "MOCK"}[prov]
	if prefix == "" {
		return fmt.Errorf("unsupported provider %q (expected anthropic|openai|ollama|mock)", prov)
	}
	vars := map[string]string{
		"LLM_PROVIDER":           prov,
		prefix + "_API_KEY":      input("api_key", ""),
		prefix + "_MODEL":        input("model", ""),
		prefix + "_BASE_URL":     input("base_url", ""),
		"GITHUB_TOKEN":           input("github_token", ""),
		"GITHUB_API_BASE_URL":    os.Getenv("GITHUB_API_URL"), // set by Actions, also for GitHub Enterprise
		"GITHUB_POST_COMMENTS":   input("post_comment", ""),
		"REVIEW_CONTEXT_FILES":   input("context_files", ""),
		"REVIEW_MAX_INPUT_BYTES": input("max_input_bytes", ""),
		"ANTHROPIC_EFFORT":       input("effort", ""),
	}
	for k, v := range vars {
		if v == "" {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("set %s: %w", k, err)
		}
	}
	return nil
}

// failThreshold converts the fail_on input into a severity rank; 0 never fails.
func failThreshold(level string) (int, error) {
	switch s := domain.Severity(strings.ToLower(level)); s {
	case "none", "":
		return 0, nil
	case domain.SeverityCritical, domain.SeverityMajor, domain.SeverityMinor, domain.SeverityInfo:
		return s.Rank(), nil
	default:
		return 0, fmt.Errorf("invalid fail_on %q (expected none|critical|major|minor|info)", level)
	}
}

func worstRank(issues []domain.Issue) int {
	worst := 0
	for _, is := range issues {
		worst = max(worst, is.Severity.Rank())
	}
	return worst
}

func appendFile(path, content string) error {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func notice(out io.Writer, format string, args ...any) {
	fmt.Fprintf(out, "::notice::%s\n", escapeCommand(fmt.Sprintf(format, args...)))
}

// escapeCommand escapes a workflow command message as GitHub requires.
func escapeCommand(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}
