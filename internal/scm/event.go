package scm

import (
	"encoding/json"
	"fmt"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

// PullRequestEvent is the subset of GitHub's pull_request event payload this
// service needs. Webhooks and GitHub Actions deliver the same payload.
// See https://docs.github.com/webhooks/webhook-events-and-payloads#pull_request
type PullRequestEvent struct {
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

// reviewableActions are pull_request actions that introduce new code.
var reviewableActions = map[string]bool{
	"opened":           true,
	"synchronize":      true,
	"reopened":         true,
	"ready_for_review": true,
}

// ParsePullRequestEvent decodes a pull_request event payload.
func ParsePullRequestEvent(body []byte) (*PullRequestEvent, error) {
	var evt PullRequestEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return nil, fmt.Errorf("decode pull_request event: %w", err)
	}
	return &evt, nil
}

// SkipReason returns why the event should not be reviewed, or "" if it should.
func (e *PullRequestEvent) SkipReason() string {
	if !reviewableActions[e.Action] {
		return fmt.Sprintf("action %q does not require review", e.Action)
	}
	if e.PullRequest.Draft {
		return "draft pull request"
	}
	return ""
}

// Ref returns the pull request the event refers to. It returns
// domain.ErrInvalidInput when the owner, repository or number is missing.
func (e *PullRequestEvent) Ref() (domain.PullRequestRef, error) {
	number := e.PullRequest.Number
	if number == 0 {
		number = e.Number
	}
	pr := domain.PullRequestRef{
		Owner:   e.Repository.Owner.Login,
		Repo:    e.Repository.Name,
		Number:  number,
		Title:   e.PullRequest.Title,
		Body:    e.PullRequest.Body,
		HeadSHA: e.PullRequest.Head.SHA,
		HTMLURL: e.PullRequest.HTMLURL,
	}
	if pr.Owner == "" || pr.Repo == "" || pr.Number <= 0 {
		return pr, fmt.Errorf("%w: event is missing repository owner, name or pull request number", domain.ErrInvalidInput)
	}
	return pr, nil
}
