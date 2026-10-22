package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/oracle-bmi/workflow-engine/internal/repository/memory"
	"example.com/oracle-bmi/workflow-engine/internal/repository/oracle"
	"example.com/oracle-bmi/workflow-engine/internal/transport/httpapi"
	"example.com/oracle-bmi/workflow-engine/internal/workflow"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var repo workflow.Repository = memory.New()
	switch mode := os.Getenv("STORE"); mode {
	case "", "memory":
		logger.Warn("using in-memory storage; data is lost on restart")
	case "oracle":
		// Register your chosen database/sql driver in this package before enabling Oracle.
		db, err := sql.Open(os.Getenv("ORACLE_DRIVER"), os.Getenv("ORACLE_DSN"))
		if err != nil {
			return fmt.Errorf("open Oracle database (register a driver first): %w", err)
		}
		defer db.Close()
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(30 * time.Minute)
		pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = db.PingContext(pingCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("ping Oracle database: %w", err)
		}
		repo = oracle.New(db)
	default:
		return fmt.Errorf("unsupported STORE %q", mode)
	}
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	srv := &http.Server{Addr: addr, Handler: http.TimeoutHandler(httpapi.New(workflow.New(repo), logger), 15*time.Second, `{"error":"request timeout"}`), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
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
