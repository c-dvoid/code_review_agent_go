package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub serves the API calls one PR review makes and records the comment.
type fakeGitHub struct {
	mu      sync.Mutex
	comment string
}

func (f *fakeGitHub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/vnd.github.v3.diff" {
			fmt.Fprint(w, "--- a/cfg.go\n+++ b/cfg.go\n@@ -1 +1 @@\n-x := 1\n+password = \"hunter2\"\n")
			return
		}
		fmt.Fprint(w, `{"head":{"sha":"abc"}}`)
	})
	mux.HandleFunc("GET /repos/o/r/pulls/1/files", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"filename":"cfg.go","status":"modified","sha":"blob1"}]`)
	})
	mux.HandleFunc("GET /repos/o/r/git/blobs/blob1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "package cfg\n\npassword = \"hunter2\"\n")
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		// What an Actions GITHUB_TOKEN gets.
		http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
	})
	mux.HandleFunc("GET /repos/o/r/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	})
	mux.HandleFunc("POST /repos/o/r/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.comment = string(b)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// setupAction sets the environment GitHub Actions provides for a pull_request run.
func setupAction(t *testing.T, apiURL, event string) (summary, output string) {
	t.Helper()
	dir := t.TempDir()
	eventPath := filepath.Join(dir, "event.json")
	if err := os.WriteFile(eventPath, []byte(event), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, output = filepath.Join(dir, "summary.md"), filepath.Join(dir, "output")
	for k, v := range map[string]string{
		"GITHUB_EVENT_NAME":   "pull_request",
		"GITHUB_EVENT_PATH":   eventPath,
		"GITHUB_API_URL":      apiURL,
		"GITHUB_STEP_SUMMARY": summary,
		"GITHUB_OUTPUT":       output,
		"INPUT_PROVIDER":      "mock",
		"INPUT_GITHUB_TOKEN":  "ghs_test",
		"INPUT_POST_COMMENT":  "true",
		"INPUT_FAIL_ON":       "none",
	} {
		t.Setenv(k, v)
	}
	return summary, output
}

const openedEvent = `{"action":"opened","number":1,"pull_request":{"number":1,"title":"t","head":{"sha":"abc"}},"repository":{"name":"r","owner":{"login":"o"}}}`

func TestRun_ReviewsPullRequest(t *testing.T) {
	gh := &fakeGitHub{}
	summary, output := setupAction(t, gh.server(t).URL, openedEvent)

	var out bytes.Buffer
	if err := run(context.Background(), &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if !strings.Contains(gh.comment, "hard-coded credential") {
		t.Errorf("comment not posted or missing the finding: %q", gh.comment)
	}
	if b, _ := os.ReadFile(summary); !strings.Contains(string(b), "Automated Code Review") {
		t.Errorf("job summary not written: %q", b)
	}
	if b, _ := os.ReadFile(output); !strings.Contains(string(b), "verdict=request_changes\nissues=1\n") {
		t.Errorf("outputs = %q", b)
	}
}

func TestRun_FailOn(t *testing.T) {
	gh := &fakeGitHub{}
	setupAction(t, gh.server(t).URL, openedEvent)
	t.Setenv("INPUT_FAIL_ON", "major")

	if err := run(context.Background(), io.Discard); !errors.Is(err, errFindings) {
		t.Fatalf("err = %v, want errFindings", err)
	}
	if gh.comment == "" {
		t.Error("the comment should still be posted when the job fails")
	}
}

func TestRun_Skips(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
		want  string
	}{
		{"push event", func(t *testing.T) { t.Setenv("GITHUB_EVENT_NAME", "push") }, "not a pull request"},
		{"draft", func(t *testing.T) {
			setupAction(t, "http://unused", `{"action":"opened","pull_request":{"draft":true}}`)
		}, "draft pull request"},
		{"fork without secrets", func(t *testing.T) {
			t.Setenv("INPUT_PROVIDER", "openai")
			t.Setenv("INPUT_API_KEY", "")
		}, "no api_key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupAction(t, "http://unused", openedEvent)
			tt.setup(t)
			var out bytes.Buffer
			if err := run(context.Background(), &out); err != nil {
				t.Fatalf("run: %v", err)
			}
			if !strings.Contains(out.String(), "::notice::") || !strings.Contains(out.String(), tt.want) {
				t.Errorf("output = %q, want a notice containing %q", out.String(), tt.want)
			}
		})
	}
}

func TestApplyInputs_RejectsUnknownProvider(t *testing.T) {
	if err := applyInputs("gemini"); err == nil {
		t.Error("want error for an unknown provider")
	}
}
