package handler

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// slogFormatter plugs log/slog into chi's RequestLogger middleware so access
// logs are emitted as structured JSON alongside the rest of the service logs.
type slogFormatter struct {
	logger *slog.Logger
}

var _ middleware.LogFormatter = (*slogFormatter)(nil)

func (f *slogFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	return &slogEntry{
		logger: f.logger.With(
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("remote_addr", r.RemoteAddr),
		),
	}
}

type slogEntry struct {
	logger *slog.Logger
}

func (e *slogEntry) Write(status, bytes int, _ http.Header, elapsed time.Duration, _ any) {
	level := slog.LevelInfo
	switch {
	case status >= 500:
		level = slog.LevelError
	case status >= 400:
		level = slog.LevelWarn
	}
	e.logger.Log(context.Background(), level, "http request",
		slog.Int("status", status),
		slog.Int("bytes", bytes),
		slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000),
	)
}

func (e *slogEntry) Panic(v any, stack []byte) {
	e.logger.Error("panic recovered",
		slog.String("panic", fmt.Sprint(v)),
		slog.String("stack", string(stack)),
	)
}

// bearerAuth requires "Authorization: Bearer <key>" when key is non-empty.
func bearerAuth(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if key == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="code-review-agent"`)
				writeError(w, r, http.StatusUnauthorized, "unauthorized", "missing or invalid API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
