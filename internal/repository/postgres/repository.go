// Package postgres persists quotes, audit events, and outbox entries using pgx.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func conflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return domain.ErrConflict
	}
	return err
}

func insertEvent(ctx context.Context, tx pgx.Tx, ev domain.Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO quote_events (quote_id, version, payload) VALUES ($1,$2,$3)`, ev.QuoteID, ev.Version, payload); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (quote_id, version, payload) VALUES ($1,$2,$3)`, ev.QuoteID, ev.Version, payload)
	return err
}

func (r *Repository) Create(ctx context.Context, q domain.Quote, ev domain.Event) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `INSERT INTO quotes (id, reference, currency, amount_minor, owner, state, version, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, q.ID, q.Reference, q.Currency, q.AmountMinor, q.Owner, q.State, q.Version, q.CreatedAt, q.UpdatedAt)
	if err != nil {
		return conflict(err)
	}
	if err = insertEvent(ctx, tx, ev); err != nil {
		return fmt.Errorf("insert event and outbox: %w", conflict(err))
	}
	return tx.Commit(ctx)
}

func (r *Repository) Get(ctx context.Context, id string) (domain.Quote, error) {
	var q domain.Quote
	err := r.pool.QueryRow(ctx, `SELECT id, reference, currency, amount_minor, owner, state, version, created_at, updated_at FROM quotes WHERE id=$1`, id).Scan(&q.ID, &q.Reference, &q.Currency, &q.AmountMinor, &q.Owner, &q.State, &q.Version, &q.CreatedAt, &q.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Quote{}, domain.ErrNotFound
	}
	return q, err
}

func (r *Repository) Update(ctx context.Context, q domain.Quote, expected int64, ev domain.Event) error {
	/*
	its call sequence:
	pool.Begin(ctx)
		→ tx.Exec(...)                update quote if version still matches
		→ insertEvent(ctx, tx, event)
			→ tx.Exec(...)            insert audit event
			→ tx.Exec(...)            insert outbox entry
		→ tx.Commit(ctx)
	*/
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(ctx, `UPDATE quotes SET reference=$1, currency=$2, amount_minor=$3, state=$4, version=$5, updated_at=$6 WHERE id=$7 AND version=$8`, q.Reference, q.Currency, q.AmountMinor, q.State, q.Version, q.UpdatedAt, q.ID, expected)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	if err = insertEvent(ctx, tx, ev); err != nil {
		return fmt.Errorf("insert event and outbox: %w", conflict(err))
	}
	return tx.Commit(ctx)
}

func (r *Repository) History(ctx context.Context, id string) ([]domain.Event, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT payload FROM quote_events WHERE quote_id=$1 ORDER BY version`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.Event, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var ev domain.Event
		if err := json.Unmarshal(payload, &ev); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

func (r *Repository) Claim(ctx context.Context, now time.Time, lease time.Duration) (outbox.Delivery, error) {
	d := outbox.Delivery{Token: uuid.NewString()}
	var payload []byte
	// SKIP LOCKED permits several workers to claim different messages. Updating
	// available_at is a lease: an abandoned claim becomes eligible after expiry.
	err := r.pool.QueryRow(ctx, `UPDATE outbox SET attempts=attempts+1, lease_token=$1, available_at=$2
 WHERE id=(SELECT id FROM outbox WHERE processed_at IS NULL AND dead_at IS NULL AND available_at <= $3 ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
 RETURNING id, payload, attempts`, d.Token, now.Add(lease), now).Scan(&d.ID, &payload, &d.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return outbox.Delivery{}, outbox.ErrEmpty
	}
	if err != nil {
		return outbox.Delivery{}, err
	}
	if err := json.Unmarshal(payload, &d.Event); err != nil {
		return outbox.Delivery{}, err
	}
	return d, nil
}

func (r *Repository) Ack(ctx context.Context, d outbox.Delivery) error {
	tag, err := r.pool.Exec(ctx, `UPDATE outbox SET processed_at=now(),lease_token=NULL,last_error=NULL WHERE id=$1 AND lease_token=$2 AND processed_at IS NULL AND dead_at IS NULL`, d.ID, d.Token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (r *Repository) Retry(ctx context.Context, d outbox.Delivery, next time.Time, reason string, dead bool) error {
	tag, err := r.pool.Exec(ctx, `UPDATE outbox SET available_at=$3, last_error=$4, lease_token=NULL, dead_at=CASE WHEN $5 THEN now() ELSE NULL END WHERE id=$1 AND lease_token=$2 AND processed_at IS NULL AND dead_at IS NULL`, d.ID, d.Token, next, reason, dead)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return nil
}
