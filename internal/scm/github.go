// Package scm contains implementations of domain.SourceControlProvider.
package scm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

// ErrCommentingDisabled is returned when a comment is requested without a token.
var ErrCommentingDisabled = errors.New("github: posting comments requires GITHUB_TOKEN")

// GitHubConfig configures the GitHub client.
type GitHubConfig struct {
	BaseURL      string
	Token        string
	MaxDiffBytes int
	HTTPClient   *http.Client
}

// GitHub implements domain.SourceControlProvider using the GitHub REST API.
type GitHub struct {
	baseURL      string
	token        string
	maxDiffBytes int
	http         *http.Client

	mu    sync.Mutex
	login string // token's user, see selfLogin
}

var _ domain.SourceControlProvider = (*GitHub)(nil)

// NewGitHub builds a GitHub client. Without a token it can still read public
// repositories but cannot post comments.
func NewGitHub(cfg GitHubConfig) *GitHub {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &GitHub{
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		token:        cfg.Token,
		maxDiffBytes: cfg.MaxDiffBytes,
		http:         hc,
	}
}

// GetPullRequestDiff implements domain.SourceControlProvider.
func (g *GitHub) GetPullRequestDiff(ctx context.Context, pr domain.PullRequestRef) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", g.baseURL, url.PathEscape(pr.Owner), url.PathEscape(pr.Repo), pr.Number)
	req, err := g.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3.diff")

	resp, err := g.http.Do(req)
	if err != nil {
		return "", wrapTransportErr(err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return "", err
	}

	// Read one byte past the limit so oversized diffs are rejected rather than
	// silently truncated (a partial diff would produce a misleading review).
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(g.maxDiffBytes)+1))
	if err != nil {
		return "", fmt.Errorf("%w: github: read diff: %v", domain.ErrProviderUnavailable, err)
	}
	if len(raw) > g.maxDiffBytes {
		return "", fmt.Errorf("%w: pull request diff is larger than %d bytes", domain.ErrInputTooLarge, g.maxDiffBytes)
	}
	return string(raw), nil
}

// maxFilePages caps pagination of the PR files endpoint, which GitHub limits
// to 3000 files (30 pages of 100).
const maxFilePages = 30

// ListPullRequestFiles implements domain.SourceControlProvider.
func (g *GitHub) ListPullRequestFiles(ctx context.Context, pr domain.PullRequestRef) ([]domain.ChangedFile, error) {
	var files []domain.ChangedFile
	for page := 1; page <= maxFilePages; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100&page=%d",
			g.baseURL, url.PathEscape(pr.Owner), url.PathEscape(pr.Repo), pr.Number, page)
		req, err := g.newRequest(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")

		var batch []struct {
			Filename string `json:"filename"`
			Status   string `json:"status"`
			SHA      string `json:"sha"`
		}
		if err := g.doJSON(req, &batch); err != nil {
			return nil, err
		}
		for _, f := range batch {
			files = append(files, domain.ChangedFile{Path: f.Filename, Status: f.Status, BlobSHA: f.SHA})
		}
		if len(batch) < 100 {
			break
		}
	}
	return files, nil
}

// GetFileContent implements domain.SourceControlProvider.
func (g *GitHub) GetFileContent(ctx context.Context, pr domain.PullRequestRef, file domain.ChangedFile, maxBytes int) (string, error) {
	if file.BlobSHA == "" {
		return "", fmt.Errorf("%w: no blob SHA for %s", domain.ErrNotFound, file.Path)
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/git/blobs/%s",
		g.baseURL, url.PathEscape(pr.Owner), url.PathEscape(pr.Repo), url.PathEscape(file.BlobSHA))
	req, err := g.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.raw+json")

	resp, err := g.http.Do(req)
	if err != nil {
		return "", wrapTransportErr(err)
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return "", err
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return "", fmt.Errorf("%w: github: read %s: %v", domain.ErrProviderUnavailable, file.Path, err)
	}
	if len(raw) > maxBytes {
		return "", fmt.Errorf("%w: %s is larger than %d bytes", domain.ErrInputTooLarge, file.Path, maxBytes)
	}
	return string(raw), nil
}

// commentMarker tags review comments so later reviews of the same PR edit the
// existing comment instead of adding a new one.
const commentMarker = "<!-- code-review-agent:review -->"

// PostReviewComment implements domain.SourceControlProvider.
func (g *GitHub) PostReviewComment(ctx context.Context, pr domain.PullRequestRef, body string) error {
	if g.token == "" {
		return ErrCommentingDisabled
	}
	if err := g.checkHead(ctx, pr); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"body": body + "\n" + commentMarker + "\n"})
	if err != nil {
		return fmt.Errorf("marshal comment: %w", err)
	}

	method := http.MethodPost
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", g.baseURL, url.PathEscape(pr.Owner), url.PathEscape(pr.Repo), pr.Number)
	existing, err := g.findReviewComment(ctx, pr)
	if err != nil {
		return err
	}
	if existing != 0 {
		method = http.MethodPatch
		endpoint = fmt.Sprintf("%s/repos/%s/%s/issues/comments/%d", g.baseURL, url.PathEscape(pr.Owner), url.PathEscape(pr.Repo), existing)
	}

	req, err := g.newRequest(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.http.Do(req)
	if err != nil {
		return wrapTransportErr(err)
	}
	defer resp.Body.Close()
	return checkStatus(resp)
}

// checkHead returns domain.ErrStaleReview when the PR has moved past the
// reviewed commit, so a slow review never overwrites a newer one.
func (g *GitHub) checkHead(ctx context.Context, pr domain.PullRequestRef) error {
	if pr.HeadSHA == "" {
		return nil
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", g.baseURL, url.PathEscape(pr.Owner), url.PathEscape(pr.Repo), pr.Number)
	req, err := g.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	var current struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := g.doJSON(req, &current); err != nil {
		return err
	}
	if current.Head.SHA != pr.HeadSHA {
		return fmt.Errorf("%w: reviewed %s, head is now %s", domain.ErrStaleReview, pr.HeadSHA, current.Head.SHA)
	}
	return nil
}

// findReviewComment returns the ID of this service's latest review comment on
// the PR, or 0 if there is none. Only comments written by the token's own user
// count, so a comment that merely copies the marker is never edited. If the
// token's user cannot be determined (e.g. a GitHub App token), it returns 0
// and a new comment is created.
func (g *GitHub) findReviewComment(ctx context.Context, pr domain.PullRequestRef) (int64, error) {
	self, err := g.selfLogin(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 0, err
		}
		return 0, nil
	}

	var found int64
	for page := 1; page <= maxCommentPages; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=100&page=%d",
			g.baseURL, url.PathEscape(pr.Owner), url.PathEscape(pr.Repo), pr.Number, page)
		req, err := g.newRequest(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")

		var batch []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := g.doJSON(req, &batch); err != nil {
			return 0, err
		}
		for _, c := range batch {
			if c.User.Login == self && strings.Contains(c.Body, commentMarker) {
				found = c.ID
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	return found, nil
}

// maxCommentPages bounds the search for an earlier review comment.
const maxCommentPages = 10

// selfLogin returns the login of the token's user, cached after the first call.
func (g *GitHub) selfLogin(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.login != "" {
		return g.login, nil
	}
	req, err := g.newRequest(ctx, http.MethodGet, g.baseURL+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	var user struct {
		Login string `json:"login"`
	}
	if err := g.doJSON(req, &user); err != nil {
		return "", err
	}
	if user.Login == "" {
		return "", fmt.Errorf("%w: github: empty login for token", domain.ErrProviderFailure)
	}
	g.login = user.Login
	return g.login, nil
}

func (g *GitHub) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "code-review-agent")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	return req, nil
}

func (g *GitHub) doJSON(req *http.Request, out any) error {
	resp, err := g.http.Do(req)
	if err != nil {
		return wrapTransportErr(err)
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: github: decode response: %v", domain.ErrProviderFailure, err)
	}
	return nil
}

func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: github: %s", domain.ErrNotFound, strings.TrimSpace(string(msg)))
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("%w: github status %d: %s", domain.ErrProviderUnavailable, resp.StatusCode, strings.TrimSpace(string(msg)))
	default:
		return fmt.Errorf("%w: github status %d: %s", domain.ErrProviderFailure, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
}

func wrapTransportErr(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: github: %v", domain.ErrProviderUnavailable, err)
}
