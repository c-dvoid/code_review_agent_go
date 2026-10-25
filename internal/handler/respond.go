package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeJSON(w, status, ErrorResponse{
		Error:     ErrorBody{Code: code, Message: msg},
		RequestID: middleware.GetReqID(r.Context()),
	})
}

// writeServiceError maps domain errors onto HTTP responses. Internal details
// are logged but not leaked to clients for 5xx responses.
func writeServiceError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	status, code, msg := http.StatusInternalServerError, "internal_error", "internal server error"

	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		status, code, msg = http.StatusBadRequest, "invalid_input", err.Error()
	case errors.Is(err, domain.ErrInputTooLarge):
		status, code, msg = http.StatusRequestEntityTooLarge, "input_too_large", err.Error()
	case errors.Is(err, domain.ErrNotFound):
		status, code, msg = http.StatusNotFound, "not_found", "resource not found"
	case errors.Is(err, domain.ErrLLMRefused):
		status, code, msg = http.StatusUnprocessableEntity, "review_refused", "the model declined to review this input"
	case errors.Is(err, domain.ErrLLMBadResponse):
		status, code, msg = http.StatusBadGateway, "bad_model_response", "the model returned an unusable response"
	case errors.Is(err, domain.ErrProviderUnavailable), errors.Is(err, domain.ErrQueueFull), errors.Is(err, domain.ErrShuttingDown):
		status, code, msg = http.StatusServiceUnavailable, "unavailable", "service temporarily unavailable, retry later"
	case errors.Is(err, domain.ErrProviderFailure):
		status, code, msg = http.StatusBadGateway, "upstream_error", "upstream provider request failed"
	case errors.Is(err, context.DeadlineExceeded):
		status, code, msg = http.StatusGatewayTimeout, "timeout", "review timed out"
	case errors.Is(err, context.Canceled):
		// Client went away; nothing useful to send.
		return
	}

	level := slog.LevelWarn
	if status >= 500 {
		level = slog.LevelError
	}
	logger.Log(r.Context(), level, "request failed",
		slog.String("request_id", middleware.GetReqID(r.Context())),
		slog.Int("status", status),
		slog.Any("error", err),
	)
	writeError(w, r, status, code, msg)
}
