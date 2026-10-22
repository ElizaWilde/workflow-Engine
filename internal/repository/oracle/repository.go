// Package oracle implements durable quote storage using an injected Oracle SQL driver.
package oracle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
)

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

func insertEvent(ctx context.Context, tx *sql.Tx, ev domain.Event) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO quote_events
		(quote_id, version, from_state, to_state, actor, occurred_at) VALUES (:1, :2, :3, :4, :5, :6)`,
		ev.QuoteID, ev.Version, string(ev.From), string(ev.To), ev.Actor, ev.At)
	return err
}

func (r *Repository) Create(ctx context.Context, q domain.Quote, ev domain.Event) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO quotes
		(id, reference, currency, amount_minor, state, version, created_at, updated_at)
		VALUES (:1, :2, :3, :4, :5, :6, :7, :8)`,
		q.ID, q.Reference, q.Currency, q.AmountMinor, string(q.State), q.Version, q.CreatedAt, q.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert quote: %w", err)
	}
	if err = insertEvent(ctx, tx, ev); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return tx.Commit()
}

func (r *Repository) Get(ctx context.Context, id string) (domain.Quote, error) {
	var q domain.Quote
	err := r.db.QueryRowContext(ctx, `SELECT id, reference, currency, amount_minor, state, version, created_at, updated_at FROM quotes WHERE id = :1`, id).
		Scan(&q.ID, &q.Reference, &q.Currency, &q.AmountMinor, &q.State, &q.Version, &q.CreatedAt, &q.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Quote{}, domain.ErrNotFound
	}
	return q, err
}

func (r *Repository) Update(ctx context.Context, q domain.Quote, expected int64, ev domain.Event) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE quotes SET state = :1, version = :2, updated_at = :3 WHERE id = :4 AND version = :5`,
		string(q.State), q.Version, q.UpdatedAt, q.ID, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.ErrConflict
	}
	if err = insertEvent(ctx, tx, ev); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return tx.Commit()
}

func (r *Repository) History(ctx context.Context, id string) ([]domain.Event, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT quote_id, version, from_state, to_state, actor, occurred_at FROM quote_events WHERE quote_id = :1 ORDER BY version`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.Event, 0)
	for rows.Next() {
		var ev domain.Event
		var from sql.NullString // Oracle treats the initial empty state as NULL.
		if err := rows.Scan(&ev.QuoteID, &ev.Version, &from, &ev.To, &ev.Actor, &ev.At); err != nil {
			return nil, err
		}
		ev.From = domain.State(from.String)
		events = append(events, ev)
	}
	return events, rows.Err()
}
