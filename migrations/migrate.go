// Package migrations applies the embedded MVP schema transactionally.
package migrations

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed 001_quotes.up.sql
var schema string

func Up(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	// Serialize concurrent application startup without relying on CREATE IF NOT EXISTS races.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(826325319)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	var applied bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 1)`).Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, schema); err != nil {
			return fmt.Errorf("apply schema version 1: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES (1)`); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
