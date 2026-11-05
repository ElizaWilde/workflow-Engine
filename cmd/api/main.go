package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/oracle-bmi/workflow-engine/internal/auth"
	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/outbox"
	"example.com/oracle-bmi/workflow-engine/internal/repository/memory"
	"example.com/oracle-bmi/workflow-engine/internal/repository/postgres"
	graph "example.com/oracle-bmi/workflow-engine/internal/transport/graphql"
	"example.com/oracle-bmi/workflow-engine/internal/transport/httpapi"
	"example.com/oracle-bmi/workflow-engine/internal/workflow"
	"example.com/oracle-bmi/workflow-engine/migrations"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

type storage interface {
	workflow.Repository
	outbox.Store
}

/*
repo := postgres.New(pool)
engine := workflow.New(repo)
api := graph.New(engine, logger)

mux.Handle("/graphql", auth.Middleware(tokens, api))

This establishes the connections:
GraphQL resolver holds → engine
Engine holds          → repository
Repository holds      → PostgreSQL connection pool
*/
func run(logger *slog.Logger) error {
	tokens, err := auth.ParseTokens(os.Getenv("AUTH_TOKENS"))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var repo storage
	ready := func(context.Context) error { return nil }
	switch mode := os.Getenv("STORE"); mode {
	case "memory":
		logger.Warn("using in-memory storage; data is lost on restart")
		repo = memory.New()
	case "", "postgres":
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return fmt.Errorf("DATABASE_URL is required for PostgreSQL; set STORE=memory for a temporary demo")
		}
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			return fmt.Errorf("configure PostgreSQL: %w", err)
		}
		defer pool.Close()
		initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = pool.Ping(initCtx)
		if err == nil {
			err = migrations.Up(initCtx, pool)
		}
		cancel()
		if err != nil {
			return fmt.Errorf("initialize PostgreSQL: %w", err)
		}
		repo, ready = postgres.New(pool), pool.Ping
	default:
		return fmt.Errorf("unsupported STORE %q", mode)
	}
	handler := outbox.Handler(func(ctx context.Context, ev domain.Event) error {
		logger.InfoContext(ctx, "quote event", "quote_id", ev.QuoteID, "version", ev.Version, "action", ev.Action)
		return nil
	})
	if endpoint := os.Getenv("EVENT_WEBHOOK_URL"); endpoint != "" {
		handler, err = outbox.Webhook(endpoint)
		if err != nil {
			return err
		}
	}
	engine := workflow.New(repo)
	rest := httpapi.New(engine, logger)
	mux := http.NewServeMux()
	mux.Handle("/", rest)
	mux.Handle("/quotes", auth.Middleware(tokens, rest))
	mux.Handle("/quotes/", auth.Middleware(tokens, rest))
	mux.Handle("/graphql", auth.Middleware(tokens, graph.New(engine, logger)))
	mux.Handle("GET /playground", playground.Handler("Quote workflow", "/graphql"))
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if err := ready(checkCtx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	srv := &http.Server{Addr: addr, Handler: http.TimeoutHandler(mux, 15*time.Second, `{"error":"request timeout"}`), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	worker := &outbox.Worker{Store: repo, Handle: handler, Logger: logger}
	go func() { defer close(workerDone); worker.Run(workerCtx) }()
	// Keep the pool open until the worker stops, including on listener failure.
	defer func() { cancelWorker(); <-workerDone }()
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	logger.Info("listening", "address", addr)
	select {
	case err := <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
			return err
		}
		return nil
	}
}
