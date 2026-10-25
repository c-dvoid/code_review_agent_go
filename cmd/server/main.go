// Command server runs the Code Review Agent HTTP service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/c-dvoid/code-review-agent-go/internal/config"
	"github.com/c-dvoid/code-review-agent-go/internal/handler"
	"github.com/c-dvoid/code-review-agent-go/internal/provider"
	"github.com/c-dvoid/code-review-agent-go/internal/scm"
	"github.com/c-dvoid/code-review-agent-go/internal/service"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	// Bootstrap logger until the configured level is known.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	level, _ := cfg.SlogLevel()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	llm, err := provider.New(cfg)
	if err != nil {
		return err
	}

	github := scm.NewGitHub(scm.GitHubConfig{
		BaseURL:      cfg.GitHub.APIBaseURL,
		Token:        cfg.GitHub.Token,
		MaxDiffBytes: cfg.Review.MaxInputBytes,
	})
	if cfg.GitHub.WebhookSecret == "" {
		logger.Warn("GITHUB_WEBHOOK_SECRET is not set; webhook signatures will NOT be verified")
	}
	postComments := cfg.GitHub.PostComments && cfg.GitHub.Token != ""
	if cfg.GitHub.PostComments && !postComments {
		logger.Warn("GITHUB_POST_COMMENTS is enabled but GITHUB_TOKEN is empty; reviews will not be posted")
	}

	reviewer := service.NewReviewer(llm, github, service.Options{
		MaxInputBytes: cfg.Review.MaxInputBytes,
		MaxTokens:     cfg.Review.MaxTokens,
		PostComments:  postComments,

		ContextFiles:        cfg.Review.ContextFiles,
		MaxContextFileBytes: cfg.Review.MaxContextFileBytes,
	}, logger)

	dispatcher := service.NewDispatcher(cfg.Webhook.MaxConcurrency, cfg.Webhook.MaxQueue, cfg.Webhook.JobTimeout, logger)

	router := handler.NewRouter(handler.RouterDeps{
		Logger: logger,
		Review: handler.NewReviewHandler(reviewer, cfg.MaxBodyBytes, logger),
		Webhook: handler.NewWebhookHandler(reviewer, dispatcher, handler.WebhookOptions{
			Secret:       cfg.GitHub.WebhookSecret,
			Async:        cfg.Webhook.Async,
			MaxBodyBytes: cfg.MaxBodyBytes,
		}, logger),
		APIKey:         cfg.APIKey,
		RequestTimeout: cfg.RequestTimeout,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Must exceed the request timeout so the Timeout middleware can reply.
		WriteTimeout: cfg.RequestTimeout + 10*time.Second,
		IdleTimeout:  120 * time.Second,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server starting",
			slog.String("addr", cfg.HTTPAddr),
			slog.String("llm_provider", llm.Name()),
			slog.Bool("webhook_async", cfg.Webhook.Async),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}
	stop() // a second signal now terminates immediately

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	// Stop accepting HTTP traffic first, then drain background reviews.
	var shutdownErr error
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server shutdown incomplete", slog.Any("error", err))
		shutdownErr = err
	}
	if err := dispatcher.Shutdown(shutdownCtx); err != nil {
		logger.Error("background reviews did not finish before shutdown deadline", slog.Any("error", err))
		shutdownErr = errors.Join(shutdownErr, err)
	}

	logger.Info("server stopped")
	return shutdownErr
}
