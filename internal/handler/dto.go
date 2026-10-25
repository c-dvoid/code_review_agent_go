package handler

import "github.com/c-dvoid/code-review-agent-go/internal/domain"

// ReviewRequest is the body of POST /api/v1/review. Exactly one of Code or
// Diff must be set.
type ReviewRequest struct {
	Code         string `json:"code,omitempty"`
	Diff         string `json:"diff,omitempty"`
	Language     string `json:"language,omitempty"`
	Filename     string `json:"filename,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

// ReviewResponse is returned by POST /api/v1/review and by the webhook in
// synchronous mode.
type ReviewResponse struct {
	RequestID string               `json:"request_id,omitempty"`
	Review    *domain.ReviewReport `json:"review"`
}

// WebhookAck is returned by the webhook when a review is queued or skipped.
type WebhookAck struct {
	Status      string `json:"status"` // accepted | ignored | pong
	Reason      string `json:"reason,omitempty"`
	DeliveryID  string `json:"delivery_id,omitempty"`
	PullRequest string `json:"pull_request,omitempty"`
}

// ErrorResponse is the uniform error envelope.
type ErrorResponse struct {
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"request_id,omitempty"`
}

// ErrorBody describes an error.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// githubPullRequestEvent is the subset of GitHub's pull_request webhook
// payload this service needs.
// See https://docs.github.com/webhooks/webhook-events-and-payloads#pull_request
type githubPullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		Draft   bool   `json:"draft"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}
