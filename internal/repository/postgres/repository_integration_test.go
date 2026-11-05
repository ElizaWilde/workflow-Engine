//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"example.com/oracle-bmi/workflow-engine/internal/auth"
	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/outbox"
	"example.com/oracle-bmi/workflow-engine/internal/repository/postgres"
	"example.com/oracle-bmi/workflow-engine/internal/workflow"
	"example.com/oracle-bmi/workflow-engine/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresMVP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is required; configure native PostgreSQL and run make integration-local")
	}
	dbConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, dbConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	// Each run gets an isolated schema. No tests touch the application's public
	// schema, even if the URL points to a development DB.
	schema := "workflow_test_" + uuid.NewString()
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatalf("create isolated test schema (the test user needs CREATE on this database): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("clean up test schema: %v", err)
		}
	})
	dbConfig.ConnConfig.RuntimeParams["search_path"] = identifier
	pool, err := pgxpool.NewWithConfig(ctx, dbConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatalf("migration is not repeatable: %v", err)
	}
	repo := postgres.New(pool)
	engine := workflow.New(repo)
	salesCtx := auth.WithPrincipal(ctx, auth.Principal{ID: "alice", Role: auth.Sales})
	approverCtx := auth.WithPrincipal(ctx, auth.Principal{ID: "bob", Role: auth.Approver})
	create := func(t *testing.T) domain.Quote {
		t.Helper()
		q, err := engine.Create(salesCtx, workflow.CreateInput{Reference: "integration", Currency: "USD", AmountMinor: 100})
		if err != nil {
			t.Fatal(err)
		}
		return q
	}

	t.Run("rollback quote and audit when outbox insert fails", func(t *testing.T) {
		q := create(t)
		// Deliberately fail the last write in the transaction for this quote only.
		_, err := pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE outbox ADD CONSTRAINT reject_test_event CHECK (quote_id <> '%s' OR version = 1)`, q.ID))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := pool.Exec(ctx, `ALTER TABLE outbox DROP CONSTRAINT reject_test_event`); err != nil {
				t.Error(err)
			}
		}()
		if _, err := engine.Execute(salesCtx, q.ID, q.Version, domain.Submit); err == nil {
			t.Fatal("expected outbox insert failure")
		}
		stored, err := engine.Get(salesCtx, q.ID)
		if err != nil || stored.Version != 1 || stored.State != domain.Draft {
			t.Fatalf("quote did not roll back: %+v %v", stored, err)
		}
		events, err := engine.History(salesCtx, q.ID)
		if err != nil || len(events) != 1 {
			t.Fatalf("audit did not roll back: %+v %v", events, err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE quote_id=$1`, q.ID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("outbox count=%d error=%v", count, err)
		}
	})

	t.Run("lifecycle edit and reconnect persistence", func(t *testing.T) {
		q := create(t)
		q, err = engine.Update(salesCtx, q.ID, workflow.UpdateInput{Version: 1, Reference: "edited", Currency: "EUR", AmountMinor: 9223372036854775807})
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range []domain.Action{domain.Submit, domain.Approve, domain.Issue} {
			actor := salesCtx
			if action == domain.Approve {
				actor = approverCtx
			}
			q, err = engine.Execute(actor, q.ID, q.Version, action)
			if err != nil {
				t.Fatal(err)
			}
		}
		pool2, err := pgxpool.NewWithConfig(ctx, dbConfig.Copy())
		if err != nil {
			t.Fatal(err)
		}
		defer pool2.Close()
		durable := workflow.New(postgres.New(pool2))
		stored, err := durable.Get(salesCtx, q.ID)
		if err != nil || stored.State != domain.Issued || stored.Version != 5 || stored.AmountMinor != q.AmountMinor || stored.Reference != "edited" {
			t.Fatalf("reconnected quote: %+v %v", stored, err)
		}
		events, err := durable.History(salesCtx, q.ID)
		if err != nil || len(events) != 5 || events[3].Actor != "bob" || events[1].Quote.AmountMinor != q.AmountMinor {
			t.Fatalf("persisted history: %+v %v", events, err)
		}
	})

	t.Run("concurrent transitions commit once", func(t *testing.T) {
		q := create(t)
		concurrent := workflow.New(repo)
		var arrived sync.WaitGroup
		arrived.Add(2)
		if err := concurrent.AddStep(workflow.Transition{From: domain.Draft, To: domain.Submitted}, func(context.Context, domain.Quote) error { arrived.Done(); arrived.Wait(); return nil }); err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() { _, err := concurrent.Execute(salesCtx, q.ID, 1, domain.Submit); results <- err }()
		}
		success, conflicts := 0, 0
		for i := 0; i < 2; i++ {
			err := <-results
			if err == nil {
				success++
			} else if errors.Is(err, domain.ErrConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || conflicts != 1 {
			t.Fatalf("success=%d conflicts=%d", success, conflicts)
		}
		events, err := engine.History(salesCtx, q.ID)
		if err != nil || len(events) != 2 {
			t.Fatalf("concurrent history: %+v %v", events, err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE quote_id=$1`, q.ID).Scan(&count); err != nil || count != 2 {
			t.Fatalf("duplicate outbox entry: count=%d error=%v", count, err)
		}
	})

	t.Run("concurrent claims and expired lease fencing", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE outbox SET processed_at=now()`); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 10; i++ {
			create(t)
		}
		now := time.Now().UTC().Add(time.Hour)
		results := make(chan outbox.Delivery, 10)
		failures := make(chan error, 10)
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				d, err := repo.Claim(ctx, now, outbox.Lease)
				if err != nil {
					failures <- err
					return
				}
				results <- d
			}()
		}
		wg.Wait()
		close(results)
		close(failures)
		for err := range failures {
			t.Error(err)
		}
		seen := make(map[int64]bool)
		for d := range results {
			if seen[d.ID] {
				t.Fatalf("duplicate claim: %d", d.ID)
			}
			seen[d.ID] = true
			if err := repo.Ack(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
		if len(seen) != 10 {
			t.Fatalf("claimed %d events", len(seen))
		}
		create(t)
		first, err := repo.Claim(ctx, now, outbox.Lease)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Claim(ctx, now, outbox.Lease); !errors.Is(err, outbox.ErrEmpty) {
			t.Fatalf("claimed active lease: %v", err)
		}
		second, err := repo.Claim(ctx, now.Add(outbox.Lease), outbox.Lease)
		if err != nil || second.ID != first.ID || second.Attempts != 2 {
			t.Fatalf("reclaim: %+v %v", second, err)
		}
		if err := repo.Ack(ctx, first); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("accepted stale acknowledgement: %v", err)
		}
		if err := repo.Retry(ctx, second, now, "delivery failed", true); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Claim(ctx, now.Add(time.Hour), outbox.Lease); !errors.Is(err, outbox.ErrEmpty) {
			t.Fatalf("claimed dead event: %v", err)
		}
		var dead bool
		if err := pool.QueryRow(ctx, `SELECT dead_at IS NOT NULL FROM outbox WHERE id=$1`, second.ID).Scan(&dead); err != nil || !dead {
			t.Fatalf("dead letter not persisted: %v %v", dead, err)
		}
	})
}
