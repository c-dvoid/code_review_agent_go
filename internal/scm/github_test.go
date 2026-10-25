package scm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

func TestGitHubFileContext(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/pulls/1/files", func(w http.ResponseWriter, r *http.Request) {
		// A full first page forces a request for the second one.
		n := 100
		if r.URL.Query().Get("page") == "2" {
			n = 1
		}
		fmt.Fprint(w, "[")
		for i := range n {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"filename":"f%s-%d.go","status":"modified","sha":"abc"}`, r.URL.Query().Get("page"), i)
		}
		fmt.Fprint(w, "]")
	})
	mux.HandleFunc("GET /repos/o/r/git/blobs/abc", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
			http.Error(w, "want raw media type", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "package f\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	gh := NewGitHub(GitHubConfig{BaseURL: srv.URL, MaxDiffBytes: 1000})
	pr := domain.PullRequestRef{Owner: "o", Repo: "r", Number: 1}
	ctx := context.Background()

	files, err := gh.ListPullRequestFiles(ctx, pr)
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	if len(files) != 101 || files[100].Path != "f2-0.go" || files[0].BlobSHA != "abc" {
		t.Fatalf("got %d files, last=%+v", len(files), files[len(files)-1])
	}

	content, err := gh.GetFileContent(ctx, pr, files[0], 100)
	if err != nil || content != "package f\n" {
		t.Fatalf("content = %q, err = %v", content, err)
	}
	if _, err := gh.GetFileContent(ctx, pr, files[0], 5); !errors.Is(err, domain.ErrInputTooLarge) {
		t.Fatalf("err = %v, want ErrInputTooLarge", err)
	}
}

// fakeCommentAPI serves the endpoints PostReviewComment touches and records writes.
type fakeCommentAPI struct {
	head     string
	comments string // JSON array returned for the PR's comments
	userErr  bool

	created, patched string // request bodies
	patchedID        string
}

func (f *fakeCommentAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"head":{"sha":%q}}`, f.head)
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if f.userErr {
			http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
			return
		}
		fmt.Fprint(w, `{"login":"bot"}`)
	})
	mux.HandleFunc("GET /repos/o/r/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, f.comments)
	})
	mux.HandleFunc("POST /repos/o/r/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.created = string(b)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("PATCH /repos/o/r/issues/comments/{id}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.patched, f.patchedID = string(b), r.PathValue("id")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestPostReviewComment(t *testing.T) {
	pr := domain.PullRequestRef{Owner: "o", Repo: "r", Number: 1, HeadSHA: "new"}
	marked := strings.ReplaceAll(commentMarker, `"`, `\"`)

	tests := []struct {
		name        string
		api         fakeCommentAPI
		fallback    string
		wantErr     error
		wantCreated bool
		wantPatchID string
	}{
		{
			name:        "first review creates a comment",
			api:         fakeCommentAPI{head: "new", comments: `[]`},
			wantCreated: true,
		},
		{
			name: "later review edits the bot's latest marked comment",
			api: fakeCommentAPI{head: "new", comments: `[
				{"id":10,"body":"old review ` + marked + `","user":{"login":"bot"}},
				{"id":11,"body":"lgtm","user":{"login":"bot"}},
				{"id":12,"body":"copied ` + marked + `","user":{"login":"mallory"}},
				{"id":13,"body":"newer review ` + marked + `","user":{"login":"bot"}}
			]`},
			wantPatchID: "13",
		},
		{
			name:    "stale review posts nothing",
			api:     fakeCommentAPI{head: "newer", comments: `[]`},
			wantErr: domain.ErrStaleReview,
		},
		{
			name:        "unknown token user falls back to a new comment",
			api:         fakeCommentAPI{head: "new", userErr: true, comments: `[{"id":10,"body":"` + marked + `","user":{"login":"bot"}}]`},
			wantCreated: true,
		},
		{
			name:        "Actions token edits the bot comment via the fallback login",
			api:         fakeCommentAPI{head: "new", userErr: true, comments: `[{"id":10,"body":"` + marked + `","user":{"login":"bot"}}]`},
			fallback:    "bot",
			wantPatchID: "10",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := tt.api
			gh := NewGitHub(GitHubConfig{BaseURL: api.server(t).URL, Token: "t", FallbackLogin: tt.fallback})

			err := gh.PostReviewComment(context.Background(), pr, "review body")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := api.created != ""; got != tt.wantCreated {
				t.Errorf("created = %v, want %v", got, tt.wantCreated)
			}
			if api.patchedID != tt.wantPatchID {
				t.Errorf("patched comment %q, want %q", api.patchedID, tt.wantPatchID)
			}
			if raw := api.created + api.patched; raw != "" {
				var c struct{ Body string }
				if err := json.Unmarshal([]byte(raw), &c); err != nil || !strings.HasSuffix(c.Body, commentMarker+"\n") {
					t.Errorf("written body lacks marker: %s (err %v)", raw, err)
				}
			}
		})
	}
}
