package domain

import "errors"

// Sentinel errors shared across layers. Wrap them with fmt.Errorf("...: %w", err)
// so handlers can map them to HTTP status codes with errors.Is.
var (
	ErrInvalidInput        = errors.New("invalid input")
	ErrInputTooLarge       = errors.New("input exceeds maximum allowed size")
	ErrLLMRefused          = errors.New("model declined to review the input")
	ErrLLMBadResponse      = errors.New("model returned an unusable response")
	ErrProviderUnavailable = errors.New("upstream provider temporarily unavailable")
	ErrProviderFailure     = errors.New("upstream provider request failed")
	ErrNotFound            = errors.New("resource not found")
	ErrShuttingDown        = errors.New("service is shutting down")
	ErrQueueFull           = errors.New("review queue is full")
	ErrStaleReview         = errors.New("pull request has newer commits than the reviewed one")
)
