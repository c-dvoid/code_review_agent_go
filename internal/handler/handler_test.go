package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
	"github.com/c-dvoid/code-review-agent-go/internal/provider"
	"github.com/c-dvoid/code-review-agent-go/internal/service"
)

const testSecret = "s3cret"

type fakeSCM struct{}

func (fakeSCM) GetPullRequestDiff(context.Context, domain.PullRequestRef) (string, error) {
	return "+++ b/a.go\n+_ = err\n", nil
}
func (fakeSCM) ListPullRequestFiles(context.Context, domain.PullRequestRef) ([]domain.ChangedFile, error) {
	return nil, nil
}
func (fakeSCM) GetFileContent(context.Context, domain.PullRequestRef, domain.ChangedFile, int) (string, error) {
	return "", domain.ErrNotFound
}
func (fakeSCM) PostReviewComment(context.Context, domain.PullRequestRef, string) error { return nil }

type syncJobs struct{ ran int }

func (j *syncJobs) Submit(_ string, fn func(context.Context)) error {
	j.ran++
	fn(context.Background())
	return nil
}

func newTestRouter(apiKey string, async bool, jobs JobRunner) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.NewReviewer(provider.NewMock(), fakeSCM{}, service.Options{MaxInputBytes: 10000, MaxTokens: 1000}, logger)
	return NewRouter(RouterDeps{
		Logger: logger,
		Review: NewReviewHandler(svc, 1<<20, logger),
		Webhook: NewWebhookHandler(svc, jobs, WebhookOptions{
			Secret: testSecret, Async: async, MaxBodyBytes: 1 << 20,
		}, logger),
		APIKey:         apiKey,
		RequestTimeout: 5 * time.Second,
	})
}

func do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestReviewEndpoint(t *testing.T) {
	h := newTestRouter("key", false, nil)

	cases := []struct {
		name, auth, body string
		want             int
	}{
		{"ok", "Bearer key", `{"code":"fmt.Println(1)","language":"go"}`, http.StatusOK},
		{"no auth", "", `{"code":"x"}`, http.StatusUnauthorized},
		{"both fields", "Bearer key", `{"code":"x","diff":"y"}`, http.StatusBadRequest},
		{"unknown field", "Bearer key", `{"source":"x"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/review", strings.NewReader(tc.body))
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := do(h, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body)
			}
			if tc.want == http.StatusOK {
				var resp ReviewResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatal(err)
				}
				if resp.Review == nil || len(resp.Review.Issues) != 1 || resp.RequestID == "" {
					t.Errorf("unexpected response: %s", rec.Body)
				}
			}
		})
	}
}

func signedWebhook(event, body string) *http.Request {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "d-1")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return req
}

const prPayload = `{"action":"opened","number":3,"pull_request":{"number":3,"title":"t","head":{"sha":"abc"}},"repository":{"name":"repo","owner":{"login":"me"}}}`

func TestGitHubWebhook(t *testing.T) {
	jobs := &syncJobs{}
	h := newTestRouter("", true, jobs)

	bad := signedWebhook("pull_request", prPayload)
	bad.Header.Set("X-Hub-Signature-256", "sha256=00")
	if rec := do(h, bad); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad signature: status = %d", rec.Code)
	}

	if rec := do(h, signedWebhook("ping", `{}`)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "pong") {
		t.Errorf("ping: %d %s", rec.Code, rec.Body)
	}

	closed := strings.Replace(prPayload, `"opened"`, `"closed"`, 1)
	if rec := do(h, signedWebhook("pull_request", closed)); !strings.Contains(rec.Body.String(), "ignored") {
		t.Errorf("closed action should be ignored: %s", rec.Body)
	}

	rec := do(h, signedWebhook("pull_request", prPayload))
	if rec.Code != http.StatusAccepted || jobs.ran != 1 {
		t.Fatalf("async: status=%d jobs=%d body=%s", rec.Code, jobs.ran, rec.Body)
	}
}

func TestGitHubWebhookSync(t *testing.T) {
	h := newTestRouter("", false, nil)
	rec := do(h, signedWebhook("pull_request", prPayload))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	var resp ReviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Review.PullRequest == nil || resp.Review.PullRequest.Number != 3 || resp.Review.Verdict != domain.VerdictRequestChanges {
		t.Errorf("unexpected review: %s", rec.Body)
	}
}
