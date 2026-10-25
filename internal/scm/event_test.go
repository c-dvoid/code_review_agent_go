package scm

import (
	"errors"
	"testing"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

func TestPullRequestEvent(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		wantSkip bool
		wantErr  error
		wantRef  domain.PullRequestRef
	}{
		{
			name:    "opened",
			payload: `{"action":"opened","number":7,"pull_request":{"number":7,"title":"t","head":{"sha":"abc"}},"repository":{"name":"r","owner":{"login":"o"}}}`,
			wantRef: domain.PullRequestRef{Owner: "o", Repo: "r", Number: 7, Title: "t", HeadSHA: "abc"},
		},
		{
			name:    "number only at the top level",
			payload: `{"action":"synchronize","number":8,"repository":{"name":"r","owner":{"login":"o"}}}`,
			wantRef: domain.PullRequestRef{Owner: "o", Repo: "r", Number: 8},
		},
		{name: "closed", payload: `{"action":"closed"}`, wantSkip: true},
		{name: "draft", payload: `{"action":"opened","pull_request":{"draft":true}}`, wantSkip: true},
		{name: "missing repository", payload: `{"action":"opened","number":1}`, wantErr: domain.ErrInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt, err := ParsePullRequestEvent([]byte(tt.payload))
			if err != nil {
				t.Fatal(err)
			}
			if skip := evt.SkipReason() != ""; skip != tt.wantSkip {
				t.Fatalf("skip = %v (%q), want %v", skip, evt.SkipReason(), tt.wantSkip)
			}
			if tt.wantSkip {
				return
			}
			ref, err := evt.Ref()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err == nil && ref != tt.wantRef {
				t.Errorf("ref = %+v, want %+v", ref, tt.wantRef)
			}
		})
	}
	if _, err := ParsePullRequestEvent([]byte("{")); err == nil {
		t.Error("want error for invalid JSON")
	}
}
