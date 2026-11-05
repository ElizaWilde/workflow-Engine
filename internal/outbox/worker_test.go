package outbox_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"example.com/oracle-bmi/workflow-engine/internal/auth"
	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/outbox"
	"example.com/oracle-bmi/workflow-engine/internal/repository/memory"
	"example.com/oracle-bmi/workflow-engine/internal/workflow"
)

func fixture(t *testing.T) *memory.Repository {
	t.Helper()
	r := memory.New()
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "alice", Role: auth.Sales})
	if _, err := workflow.New(r).Create(ctx, workflow.CreateInput{Reference: "x", Currency: "USD", AmountMinor: 100}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRetryBackoffAndDeadLetter(t *testing.T) {
	repo := fixture(t)
	now := time.Now().UTC()
	calls := 0
	worker := outbox.Worker{Store: repo, Now: func() time.Time { return now }, Handle: func(context.Context, domain.Event) error { calls++; return errors.New("unavailable") }}
	for attempt := 1; attempt <= outbox.MaxAttempts; attempt++ {
		worked, err := worker.ProcessOne(context.Background())
		if err != nil || !worked || calls != attempt {
			t.Fatalf("attempt %d: worked=%v calls=%d error=%v", attempt, worked, calls, err)
		}
		if worked, err := worker.ProcessOne(context.Background()); err != nil || worked {
			t.Fatalf("retried before backoff: %v %v", worked, err)
		}
		now = now.Add(time.Hour)
	}
	if worked, err := worker.ProcessOne(context.Background()); err != nil || worked {
		t.Fatalf("dead event was redelivered: %v %v", worked, err)
	}
}

func TestExpiredLeaseRejectsOldAcknowledgement(t *testing.T) {
	repo := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	first, err := repo.Claim(ctx, now, outbox.Lease)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Claim(ctx, now, outbox.Lease); !errors.Is(err, outbox.ErrEmpty) {
		t.Fatalf("claim held lease: %v", err)
	}
	second, err := repo.Claim(ctx, now.Add(outbox.Lease), outbox.Lease)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Token == first.Token || second.Attempts != 2 {
		t.Fatalf("lease was not renewed: %+v %+v", first, second)
	}
	if err := repo.Ack(ctx, first); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale ack: %v", err)
	}
	if err := repo.Ack(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Claim(ctx, now.Add(time.Hour), outbox.Lease); !errors.Is(err, outbox.ErrEmpty) {
		t.Fatalf("acked event redelivered: %v", err)
	}
}

func TestWebhookRetryKeepsIdempotencyKey(t *testing.T) {
	calls, key := 0, ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong webhook request")
		}
		if calls == 1 {
			key = r.Header.Get("Idempotency-Key")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if key == "" || r.Header.Get("Idempotency-Key") != key {
			t.Error("idempotency key changed")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	handler, err := outbox.Webhook(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	worker := outbox.Worker{Store: fixture(t), Handle: handler, Now: func() time.Time { return now }}
	if _, err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("webhook calls=%d", calls)
	}
	if worked, err := worker.ProcessOne(context.Background()); err != nil || worked {
		t.Fatalf("delivered event retried: %v %v", worked, err)
	}
}
