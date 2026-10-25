package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
	"github.com/c-dvoid/code-review-agent-go/internal/scm"
)

// JobRunner executes work in the background, detached from the request.
type JobRunner interface {
	Submit(name string, fn func(ctx context.Context)) error
}

// WebhookHandler receives source control webhooks.
type WebhookHandler struct {
	svc          domain.ReviewService
	jobs         JobRunner
	secret       []byte
	async        bool
	maxBodyBytes int64
	logger       *slog.Logger
}

// WebhookOptions configures a WebhookHandler.
type WebhookOptions struct {
	// Secret verifies X-Hub-Signature-256. Empty disables verification.
	Secret       string
	Async        bool
	MaxBodyBytes int64
}

// NewWebhookHandler builds a WebhookHandler. jobs may be nil when Async is false.
func NewWebhookHandler(svc domain.ReviewService, jobs JobRunner, opts WebhookOptions, logger *slog.Logger) *WebhookHandler {
	return &WebhookHandler{
		svc:          svc,
		jobs:         jobs,
		secret:       []byte(opts.Secret),
		async:        opts.Async,
		maxBodyBytes: opts.MaxBodyBytes,
		logger:       logger,
	}
}

// GitHub handles POST /api/v1/webhook/github.
func (h *WebhookHandler) GitHub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
			return
		}
		writeError(w, r, http.StatusBadRequest, "invalid_body", "failed to read request body")
		return
	}
	if !h.validSignature(body, r.Header.Get("X-Hub-Signature-256")) {
		writeError(w, r, http.StatusUnauthorized, "invalid_signature", "webhook signature verification failed")
		return
	}

	deliveryID := r.Header.Get("X-GitHub-Delivery")
	switch event := r.Header.Get("X-GitHub-Event"); event {
	case "ping":
		writeJSON(w, http.StatusOK, WebhookAck{Status: "pong", DeliveryID: deliveryID})
		return
	case "pull_request":
	default:
		writeJSON(w, http.StatusOK, WebhookAck{Status: "ignored", Reason: fmt.Sprintf("unsupported event %q", event), DeliveryID: deliveryID})
		return
	}

	evt, err := scm.ParsePullRequestEvent(body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "invalid pull_request payload: "+err.Error())
		return
	}
	if reason := evt.SkipReason(); reason != "" {
		writeJSON(w, http.StatusOK, WebhookAck{Status: "ignored", Reason: reason, DeliveryID: deliveryID})
		return
	}
	pr, err := evt.Ref()
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_input", "payload is missing repository owner, name or pull request number")
		return
	}
	prName := fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)

	if !h.async {
		report, err := h.svc.ReviewPullRequest(r.Context(), pr)
		if err != nil {
			writeServiceError(w, r, h.logger, err)
			return
		}
		writeJSON(w, http.StatusOK, ReviewResponse{RequestID: middleware.GetReqID(r.Context()), Review: report})
		return
	}

	logger := h.logger.With(
		slog.String("request_id", middleware.GetReqID(r.Context())),
		slog.String("delivery_id", deliveryID),
		slog.String("pull_request", prName),
		slog.String("head_sha", pr.HeadSHA),
	)
	err = h.jobs.Submit("review "+prName, func(ctx context.Context) {
		report, err := h.svc.ReviewPullRequest(ctx, pr)
		if err != nil {
			logger.ErrorContext(ctx, "pull request review failed", slog.Any("error", err))
			return
		}
		logger.InfoContext(ctx, "pull request review finished",
			slog.String("review_id", report.ID),
			slog.String("verdict", string(report.Verdict)),
			slog.Bool("comment_posted", report.CommentPosted),
		)
	})
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}

	logger.Info("pull request review queued")
	writeJSON(w, http.StatusAccepted, WebhookAck{Status: "accepted", DeliveryID: deliveryID, PullRequest: prName})
}

func (h *WebhookHandler) validSignature(body []byte, header string) bool {
	if len(h.secret) == 0 {
		return true
	}
	sigHex, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, h.secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
