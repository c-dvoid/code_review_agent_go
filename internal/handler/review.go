package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

// ReviewHandler serves the manual review endpoint.
type ReviewHandler struct {
	svc          domain.ReviewService
	maxBodyBytes int64
	logger       *slog.Logger
}

// NewReviewHandler builds a ReviewHandler.
func NewReviewHandler(svc domain.ReviewService, maxBodyBytes int64, logger *slog.Logger) *ReviewHandler {
	return &ReviewHandler{svc: svc, maxBodyBytes: maxBodyBytes, logger: logger}
}

// Review handles POST /api/v1/review.
func (h *ReviewHandler) Review(w http.ResponseWriter, r *http.Request) {
	var req ReviewRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, h.maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
			return
		}
		writeError(w, r, http.StatusBadRequest, "invalid_json", "invalid JSON body: "+err.Error())
		return
	}

	hasCode, hasDiff := strings.TrimSpace(req.Code) != "", strings.TrimSpace(req.Diff) != ""
	if hasCode == hasDiff {
		writeError(w, r, http.StatusBadRequest, "invalid_input", `exactly one of "code" or "diff" must be provided`)
		return
	}

	in := domain.ReviewInput{
		Kind:         domain.InputKindCode,
		Content:      req.Code,
		Language:     req.Language,
		Filename:     req.Filename,
		Instructions: req.Instructions,
	}
	if hasDiff {
		in.Kind, in.Content = domain.InputKindDiff, req.Diff
	}

	report, err := h.svc.Review(r.Context(), in)
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, ReviewResponse{
		RequestID: middleware.GetReqID(r.Context()),
		Review:    report,
	})
}
