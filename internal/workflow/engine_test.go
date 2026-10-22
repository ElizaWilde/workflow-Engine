package workflow_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/repository/memory"
	"example.com/oracle-bmi/workflow-engine/internal/workflow"
)

func fixture(t *testing.T) (*workflow.Engine, domain.Quote) {
	t.Helper()
	e := workflow.New(memory.New())
	q, err := e.Create(context.Background(), workflow.CreateInput{Reference: "BMI-001", Currency: "USD", AmountMinor: 12500, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return e, q
}

func TestLifecycleAndHistory(t *testing.T) {
	e, q := fixture(t)
	ctx := context.Background()
	for _, state := range []domain.State{domain.Submitted, domain.Approved, domain.Issued} {
		var err error
		q, err = e.Move(ctx, q.ID, q.Version, state, "tester")
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Move(ctx, q.ID, q.Version, domain.Cancelled, "tester"); !errors.Is(err, domain.ErrTransition) {
		t.Fatalf("terminal state: %v", err)
	}
	events, err := e.History(ctx, q.ID)
	if err != nil || len(events) != 4 {
		t.Fatalf("history = %v, %v", events, err)
	}
	for i, ev := range events {
		if ev.Version != int64(i+1) || ev.QuoteID != q.ID {
			t.Fatalf("bad event: %+v", ev)
		}
		if i > 0 && ev.From != events[i-1].To {
			t.Fatalf("broken audit chain: %+v", ev)
		}
	}
}

func TestFailedStepDoesNotPersist(t *testing.T) {
	e, q := fixture(t)
	want := errors.New("BMI validation unavailable")
	if err := e.AddStep(workflow.Transition{From: domain.Draft, To: domain.Submitted}, func(context.Context, domain.Quote) error { return want }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Move(context.Background(), q.ID, 1, domain.Submitted, "tester"); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	stored, err := e.Get(context.Background(), q.ID)
	if err != nil || stored != q {
		t.Fatalf("quote changed: %+v, %v", stored, err)
	}
	events, err := e.History(context.Background(), q.ID)
	if err != nil || len(events) != 1 {
		t.Fatalf("history changed: %v, %v", events, err)
	}
}

func TestConcurrentTransitionsCommitOnce(t *testing.T) {
	e, q := fixture(t)
	// Force both requests to read version 1 before either can commit.
	var arrived sync.WaitGroup
	arrived.Add(2)
	if err := e.AddStep(workflow.Transition{From: domain.Draft, To: domain.Submitted}, func(context.Context, domain.Quote) error { arrived.Done(); arrived.Wait(); return nil }); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := e.Move(context.Background(), q.ID, 1, domain.Submitted, "tester"); results <- err }()
	}
	successes, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	events, err := e.History(context.Background(), q.ID)
	if err != nil || len(events) != 2 {
		t.Fatalf("history = %v, %v", events, err)
	}
}

func TestValidationAndMissingQuote(t *testing.T) {
	e, _ := fixture(t)
	for _, in := range []workflow.CreateInput{
		{Reference: "x", Currency: "usd", Actor: "tester"},
		{Reference: "x", Currency: "USD", AmountMinor: -1, Actor: "tester"},
		{Reference: "x", Currency: "USD"},
	} {
		if _, err := e.Create(context.Background(), in); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("input %+v: %v", in, err)
		}
	}
	if _, err := e.Get(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
}
