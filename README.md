# Code Review Agent (Go)

AI code review for your pull requests. Add one workflow file to a repository and every pull request gets a review comment that points out bugs, security problems and risky changes, and is updated on each push. Bring your own LLM key (Anthropic, OpenRouter, OpenAI or a local Ollama); nothing to host.

## Quick start: review every pull request (GitHub Action)

Nothing to install or host. GitHub runs the review on its own machines whenever a pull request is opened or updated, and `github-actions[bot]` posts the result as a comment that is updated on each push. It works in public and private repositories, for any programming language.

**1. Get an API key** from [Anthropic](https://console.anthropic.com/settings/keys), or from [OpenRouter](https://openrouter.ai/keys) to use other models.

**2. Save it as a repository secret.** In your repository: Settings → Secrets and variables → Actions → **New repository secret**. Name it `ANTHROPIC_API_KEY` (or `OPENROUTER_API_KEY`). The name must match the one used in the workflow below.

**3. Add the workflow.** Click Add file → Create new file, name it `.github/workflows/code-review.yml`, paste one of the following and commit it to your default branch.

With Anthropic:

```yaml
name: Code review

on:
  pull_request:
    types: [opened, synchronize, reopened, ready_for_review]

permissions:
  contents: read        # read the diff and changed files
  pull-requests: write  # post and update the review comment

concurrency:            # a new push cancels the review of the previous one
  group: code-review-${{ github.event.pull_request.number }}
  cancel-in-progress: true

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: c-dvoid/code_review_agent_go@v1
        with:
          api_key: ${{ secrets.ANTHROPIC_API_KEY }}
```

With OpenRouter (or any OpenAI-compatible API), use the same file with this `with:` block instead:

```yaml
        with:
          provider: openai
          base_url: https://openrouter.ai/api/v1
          model: openai/gpt-6-luna
          api_key: ${{ secrets.OPENROUTER_API_KEY }}
```

**4. Open a pull request.** After one to two minutes (most of it GitHub building the action) a review comment appears on it. The same review is shown on the run's Summary page (Actions tab).

**Troubleshooting**

| You see | Cause and fix |
|---|---|
| The job ends with "Skipping: no api_key" | The secret is missing or its name doesn't match the workflow. Pull requests from forks never receive secrets, so they are always skipped. |
| "Skipping: draft pull request" | Drafts are reviewed once they are marked ready for review. |
| 403 "Resource not accessible by integration" when posting | Settings → Actions → General → Workflow permissions: choose "Read and write permissions". |
| 401 or 404 from the LLM | Wrong key, model ID or `base_url`. |
| No job runs at all | The workflow file must be in `.github/workflows/` on the default branch, and Actions must be enabled (Settings → Actions). |

### Options

| Input | Default | Description |
|---|---|---|
| `api_key` | – | LLM API key (use a secret) |
| `provider` | `anthropic` | `anthropic`, `openai` (any OpenAI-compatible API) or `ollama` |
| `model` | `claude-opus-5-5` for anthropic | Model ID; required for `openai` |
| `base_url` | provider default | API base URL, e.g. `https://openrouter.ai/api/v1` |
| `effort` | `high` | Anthropic only: `low` … `max` |
| `post_comment` | `true` | `false` writes the review to the job summary only |
| `context_files` | `true` | Send the full changed files, not just the diff |
| `max_input_bytes` | `400000` | Larger diffs are not reviewed |
| `fail_on` | `none` | Fail the job (e.g. to block merging) on an issue at or above `critical`, `major`, `minor` or `info` |
| `github_token` | `${{ github.token }}` | Token for reading the PR and posting the comment |

Outputs: `verdict`, `issues` and `review_id`. Every run also writes the review to the job summary.

Notes:
- **Pull requests from forks** are skipped (GitHub doesn't give them secrets). Don't work around it with `pull_request_target` unless you understand the risks.
- **Cost:** each repository pays for its own LLM usage with its own key.
- **Privacy:** the diff and the changed files are sent to the LLM provider you configure, and nowhere else.
- The action never reads a `.env` file, even if the workspace has one, so a pull request can't redirect the API key to another server.

## Self-hosting and development

The same review service also runs as an HTTP server: on-demand reviews through a REST endpoint, and pull request reviews through a GitHub webhook. You only need this to run it on your own infrastructure or to work on the code; the GitHub Action above needs none of it.

- **Router:** `go-chi/chi/v5` with the `RequestID`, `RequestLogger` (slog JSON), `Recoverer` and `Timeout` middlewares
- **Logging:** `log/slog` JSON to stdout
- **Config:** environment variables (`caarlos0/env/v10`)
- **LLM providers:** Anthropic Claude (official Go SDK), OpenAI, Ollama (any OpenAI-compatible server) and an offline mock
- **Graceful shutdown:** on SIGINT/SIGTERM the service drains HTTP requests first, then finishes background reviews

## Project layout

```
cmd/server/main.go          wiring, HTTP server, signal handling, graceful shutdown
cmd/action/main.go          GitHub Action entry point: one PR review per workflow run
action.yml                  GitHub Action definition (built from Dockerfile.action)
internal/config/            env-based configuration + validation
internal/domain/            models, sentinel errors, interfaces (LLMProvider, SourceControlProvider, ReviewService)
internal/service/           review orchestration, prompt, JSON parsing, PR comment formatting, background dispatcher
internal/provider/          LLMProvider implementations: anthropic.go, openai.go (OpenAI/Ollama), mock.go, factory.go
internal/scm/               SourceControlProvider implementation for GitHub (diff fetch + PR comments), pull_request event parsing
internal/handler/           chi router, handlers, DTOs, middleware
```

## Core interfaces (`internal/domain/interfaces.go`)

```go
type LLMProvider interface {
    Name() string
    Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}

type SourceControlProvider interface {
    GetPullRequestDiff(ctx context.Context, pr PullRequestRef) (string, error)
    PostReviewComment(ctx context.Context, pr PullRequestRef, body string) error
}

type ReviewService interface {
    Review(ctx context.Context, in ReviewInput) (*ReviewReport, error)
    ReviewPullRequest(ctx context.Context, pr PullRequestRef) (*ReviewReport, error)
}
```

To add a new backend, implement `LLMProvider` and register it in `internal/provider/factory.go`. Providers don't parse anything themselves: the service builds the prompt, passes in a JSON Schema, and parses and normalizes whatever comes back.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `HTTP_ADDR` | `:8080` | Listen address |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `REQUEST_TIMEOUT` | `180s` | Per-request timeout (chi `Timeout` middleware) |
| `SHUTDOWN_TIMEOUT` | `30s` | Grace period for draining requests and background reviews |
| `MAX_BODY_BYTES` | `2097152` | Max HTTP request body |
| `API_KEY` | – | If set, `POST /api/v1/review` requires `Authorization: Bearer <API_KEY>` |
| `LLM_PROVIDER` | `anthropic` | `anthropic` / `openai` / `ollama` / `mock` |
| `REVIEW_MAX_INPUT_BYTES` | `400000` | Inputs or PR diffs larger than this are rejected (413), never truncated |
| `REVIEW_MAX_TOKENS` | `16000` | Max output tokens per review |
| `REVIEW_CONTEXT_FILES` | `true` | For PR reviews, also send the full new version of each changed file so the model sees the code around each hunk |
| `REVIEW_MAX_CONTEXT_FILE_BYTES` | `100000` | Changed files larger than this are left out of the context (the diff is still reviewed) |
| `ANTHROPIC_API_KEY` | – | Required for `anthropic` |
| `ANTHROPIC_MODEL` | `claude-opus-5-5` | Claude model ID |
| `ANTHROPIC_EFFORT` | `high` | `low` / `medium` / `high` / `xhigh` / `max` |
| `ANTHROPIC_FALLBACKS` | `true` | Server-side refusal fallbacks. Only supported on `claude-opus-5-5`, `claude-opus-5`, `claude-fable-5-1` and `claude-sonnet-5-5`; set to `false` for any other model |
| `ANTHROPIC_BASE_URL` | – | Optional API base URL override |
| `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_BASE_URL` | –, –, `https://api.openai.com/v1` | For `openai` |
| `OLLAMA_MODEL`, `OLLAMA_BASE_URL` | –, `http://localhost:11434/v1` | For `ollama` |
| `GITHUB_TOKEN` | – | Needed for private repos and for posting comments |
| `GITHUB_WEBHOOK_SECRET` | – | Verifies `X-Hub-Signature-256`. **Set this in production** |
| `GITHUB_POST_COMMENTS` | `true` | Post the review back to the PR (requires `GITHUB_TOKEN`) |
| `GITHUB_API_BASE_URL` | `https://api.github.com` | Change this for GitHub Enterprise |
| `WEBHOOK_ASYNC` | `true` | Reply `202` right away and review in the background (GitHub expects a reply within 10s) |
| `WEBHOOK_MAX_CONCURRENCY` | `4` | Max number of reviews running at once |
| `WEBHOOK_MAX_QUEUE` | `100` | Max queued and running reviews; beyond this the webhook returns `503` |
| `WEBHOOK_JOB_TIMEOUT` | `10m` | Timeout for each background review |

Settings are read from environment variables. A `.env` file in the directory you run from is loaded too (copy `.env.example` to `.env`); real environment variables take precedence over it. `.env.example` itself is only a template and is never read.

## Running

```bash
# Offline, no API key needed
LLM_PROVIDER=mock go run ./cmd/server

# Anthropic Claude
export ANTHROPIC_API_KEY=sk-ant-...
go run ./cmd/server

# Ollama (local)
LLM_PROVIDER=ollama OLLAMA_MODEL=qwen2.5-coder go run ./cmd/server

# Tests
go test -race ./...

# Docker
docker build -t code-review-agent .
docker run -p 8080:8080 -e ANTHROPIC_API_KEY=sk-ant-... code-review-agent
```

PowerShell: `$env:LLM_PROVIDER="mock"; go run ./cmd/server`

## API

### `POST /api/v1/review`: manual review

Request (send exactly one of `code` or `diff`):

```json
{
  "code": "func div(a, b int) int { return a / b }",
  "language": "go",
  "filename": "math.go",
  "instructions": "Focus on edge cases."
}
```

```bash
curl -s localhost:8080/api/v1/review \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"diff":"--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-x := 1\n+password = \"hunter2\"","language":"go"}'
```

Response `200`:

```json
{
  "request_id": "host/abc-000001",
  "review": {
    "id": "rev_4eee72886cbeb7765910ec6a",
    "summary": "…",
    "verdict": "request_changes",
    "issues": [
      {
        "severity": "critical",
        "impact": "security",
        "likelihood": "common",
        "category": "security",
        "file": "x.go",
        "line": 1,
        "title": "Hard-coded credential",
        "description": "…",
        "suggestion": "…"
      }
    ],
    "provider": "anthropic",
    "model": "claude-opus-5-5",
    "usage": { "input_tokens": 812, "output_tokens": 304 },
    "duration_ms": 9312,
    "created_at": "2026-10-24T14:23:17Z"
  }
}
```

`verdict` is one of `approve`, `comment` or `request_changes`. `severity` is one of `critical`, `major`, `minor` or `info`, and issues come back sorted most severe first.

Severity is not picked by the model. The model classifies each issue by `impact` (`security`, `data_loss`, `crash`, `incorrect_behavior`, `resource`, `performance`, `maintainability`) and `likelihood` (`common` or `edge_case`), and the service maps that to a severity with a fixed table (`internal/service/severity.go`), so the same problem gets the same severity across runs and models. The verdict is then derived from the issues: `request_changes` if any is critical or major, `comment` if there are only minor or info issues, `approve` if there are none. If a provider returns no usable classification, the model's own `severity` is used instead.

Errors all use the same shape: `{"error":{"code":"invalid_input","message":"…"},"request_id":"…"}`.
Status codes: `400` invalid input · `401` bad API key or signature · `413` too large · `422` the model declined · `502` upstream or bad model output · `503` provider overloaded or queue full · `504` timeout.

### `POST /api/v1/webhook/github`: GitHub webhook

Handles `ping` and `pull_request` events. Only the `opened`, `synchronize`, `reopened` and `ready_for_review` actions trigger a review, and draft PRs are skipped. For a reviewable event, the service fetches the PR diff from the GitHub API, reviews it, and (when `GITHUB_TOKEN` is set) posts a Markdown comment on the PR.

- Async mode (default): replies `202 {"status":"accepted","delivery_id":"…","pull_request":"owner/repo#12"}`
- Sync mode (`WEBHOOK_ASYNC=false`): replies `200` with the full `ReviewResponse`. Use this for testing or for callers other than GitHub
- Events that aren't reviewed: `200 {"status":"ignored","reason":"…"}`

**GitHub setup:** Repo → Settings → Webhooks → Add webhook
- Payload URL: `https://<host>/api/v1/webhook/github`
- Content type: `application/json`
- Secret: same value as `GITHUB_WEBHOOK_SECRET`
- Events: *Pull requests*

For a local test, expose the port with a tunnel (e.g. `ngrok http 8080`), or send a signed payload by hand:

```bash
SECRET=s3cret
BODY='{"action":"opened","number":1,"pull_request":{"number":1,"title":"Test","head":{"sha":"abc"}},"repository":{"name":"Hello-World","owner":{"login":"octocat"}}}'
SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')

curl -s localhost:8080/api/v1/webhook/github \
  -H 'Content-Type: application/json' \
  -H 'X-GitHub-Event: pull_request' \
  -H 'X-GitHub-Delivery: test-1' \
  -H "X-Hub-Signature-256: sha256=$SIG" \
  -d "$BODY"
```

(Start the server with `GITHUB_WEBHOOK_SECRET=s3cret`. `octocat/Hello-World#1` is a public PR, so the diff fetch works without a token.)

### `GET /healthz`

Liveness probe: `{"status":"ok"}`.

## Design notes

- **Claude provider:** uses the official `anthropic-sdk-go`. It sets `output_config.effort` and a JSON-Schema `output_config.format`, so the review always comes back as valid JSON. It also enables `fallbacks: "default"`, so a safety-classifier refusal is retried server-side on another model. Any refusal that remains is returned as `422`.
- **OpenAI and Ollama:** both go through one Chat Completions client in `json_object` mode. The JSON shape is described in the system prompt, and the parser can pull JSON out of prose or code fences.
- **Prompt injection:** submitted code and PR descriptions are wrapped in `<code_to_review>` tags, and the system prompt tells the model to treat that content as data, not instructions.
- **Oversized input:** diffs over the size limit are rejected rather than truncated. A review of a partial diff would be misleading.
- **File context:** for PR reviews the service also fetches the full new version of each changed file (via the blob SHAs from the PR files API) and sends it, with line numbers, in a separate `<file_context>` block. Many real bugs only show up when the change is read together with unchanged code. Context is best-effort: removed, binary and oversized files are skipped, the diff plus context stays within `REVIEW_MAX_INPUT_BYTES`, and if fetching fails the review falls back to the diff alone.
- **Comment failures:** if posting the PR comment fails, the error is logged and the review is still returned. The LLM call has already been paid for.
- **Context:** the request context flows from the handler through the service to the provider. Background reviews use their own context, so they outlive the webhook request. That context is cancelled if graceful shutdown runs past `SHUTDOWN_TIMEOUT`.
