// Package handler contains the chi router and HTTP handlers.
package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// RouterDeps are the dependencies needed to build the HTTP router.
type RouterDeps struct {
	Logger         *slog.Logger
	Review         *ReviewHandler
	Webhook        *WebhookHandler
	APIKey         string
	RequestTimeout time.Duration
}

// NewRouter builds the HTTP router with standard middlewares.
func NewRouter(d RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RequestLogger(&slogFormatter{logger: d.Logger}))
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(d.RequestTimeout))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.With(bearerAuth(d.APIKey)).Post("/review", d.Review.Review)
		// Webhooks authenticate via HMAC signature, not the API key.
		r.Post("/webhook/github", d.Webhook.GitHub)
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	return r
}
