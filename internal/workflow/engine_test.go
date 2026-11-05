package workflow_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"example.com/oracle-bmi/workflow-engine/internal/auth"
	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/repository/memory"
	"example.com/oracle-bmi/workflow-engine/internal/workflow"
)

func fixture(t *testing.T) (*workflow.Engine, domain.Quote) {
	t.Helper()
	e := workflow.New(memory.New())
	q, err := e.Create(salesContext(), workflow.CreateInput{Reference: "BMI-001", Currency: "USD", AmountMinor: 12500})
	if err != nil {
		t.Fatal(err)
	}
	return e, q
}

func TestLifecycleAndHistory(t *testing.T) {
	e, q := fixture(t)
	ctx := salesContext()
	for _, state := range []domain.State{domain.Submitted, domain.Approved, domain.Issued} {
		var err error
		moveCtx := ctx
		if state == domain.Approved {
			moveCtx = auth.WithPrincipal(ctx, auth.Principal{ID: "manager", Role: auth.Approver})
		}
		q, err = e.Move(moveCtx, q.ID, q.Version, state)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Move(ctx, q.ID, q.Version, domain.Cancelled); !errors.Is(err, domain.ErrTransition) {
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
	if _, err := e.Move(salesContext(), q.ID, 1, domain.Submitted); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	stored, err := e.Get(salesContext(), q.ID)
	if err != nil || stored != q {
		t.Fatalf("quote changed: %+v, %v", stored, err)
	}
	events, err := e.History(salesContext(), q.ID)
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
		go func() { _, err := e.Move(salesContext(), q.ID, 1, domain.Submitted); results <- err }()
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
	events, err := e.History(salesContext(), q.ID)
	if err != nil || len(events) != 2 {
		t.Fatalf("history = %v, %v", events, err)
	}
}

func TestValidationAndMissingQuote(t *testing.T) {
	e, _ := fixture(t)
	for _, in := range []workflow.CreateInput{
		{Reference: "x", Currency: "usd"},
		{Reference: "x", Currency: "USD", AmountMinor: -1},
		{Reference: "", Currency: "USD"},
	} {
		if _, err := e.Create(salesContext(), in); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("input %+v: %v", in, err)
		}
	}
	if _, err := e.Get(salesContext(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
}

func salesContext() context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{ID: "test", Role: auth.Sales})
}

func TestDraftUpdateAndPricingRules(t *testing.T) {
	e, q := fixture(t)
	ctx := salesContext()
	updated, err := e.Update(ctx, q.ID, workflow.UpdateInput{Version: q.Version, Reference: "revised", Currency: "EUR", AmountMinor: 0})
	if err != nil || updated.Version != 2 || updated.Reference != "revised" || updated.AmountMinor != 0 {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if _, err := e.Execute(ctx, q.ID, updated.Version, domain.Submit); !errors.Is(err, domain.ErrRule) {
		t.Fatalf("zero-price submission: %v", err)
	}
	if _, err := e.Update(ctx, q.ID, workflow.UpdateInput{Version: 1, Reference: "stale", Currency: "EUR", AmountMinor: 1}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	updated, err = e.Update(ctx, q.ID, workflow.UpdateInput{Version: 2, Reference: "revised", Currency: "EUR", AmountMinor: 25})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := e.Execute(ctx, q.ID, updated.Version, domain.Submit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Update(ctx, q.ID, workflow.UpdateInput{Version: submitted.Version, Reference: "frozen", Currency: "EUR", AmountMinor: 1}); !errors.Is(err, domain.ErrTransition) {
		t.Fatalf("submitted edit: %v", err)
	}
	events, err := e.History(ctx, q.ID)
	if err != nil || len(events) != 4 || events[1].Action != domain.Update || events[1].Quote.AmountMinor != 0 || events[2].Quote.AmountMinor != 25 {
		t.Fatalf("history snapshots: %+v %v", events, err)
	}
}

func TestPermissionsAndApprovalSeparation(t *testing.T) {
	e, q := fixture(t)
	ctx := salesContext()
	other := auth.WithPrincipal(ctx, auth.Principal{ID: "other", Role: auth.Sales})
	approver := auth.WithPrincipal(ctx, auth.Principal{ID: "manager", Role: auth.Approver})
	if _, err := e.Get(context.Background(), q.ID); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("unauthenticated read: %v", err)
	}
	if _, err := e.Get(other, q.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("other owner read: %v", err)
	}
	if _, err := e.History(other, q.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("other owner history: %v", err)
	}
	if _, err := e.Execute(other, q.ID, 1, domain.Submit); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("other owner submit: %v", err)
	}
	if _, err := e.Create(approver, workflow.CreateInput{Reference: "x", Currency: "USD"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("approver create: %v", err)
	}
	if _, err := e.Update(approver, q.ID, workflow.UpdateInput{Version: 1, Reference: "x", Currency: "USD"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("approver edit: %v", err)
	}
	var err error
	q, err = e.Execute(ctx, q.ID, 1, domain.Submit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, q.ID, q.Version, domain.Approve); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("sales approve: %v", err)
	}
	ownerAdmin := auth.WithPrincipal(ctx, auth.Principal{ID: q.Owner, Role: auth.Admin})
	if _, err := e.Execute(ownerAdmin, q.ID, q.Version, domain.Approve); !errors.Is(err, domain.ErrRule) {
		t.Fatalf("self approval: %v", err)
	}
	if _, err := e.Execute(approver, q.ID, q.Version, domain.Issue); !errors.Is(err, domain.ErrTransition) {
		t.Fatalf("issue before approval: %v", err)
	}
	q, err = e.Execute(approver, q.ID, q.Version, domain.Approve)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(approver, q.ID, q.Version, domain.Issue); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("approver issue: %v", err)
	}
	if _, err := e.Execute(ctx, q.ID, q.Version, domain.Issue); err != nil {
		t.Fatal(err)
	}
}

func TestRejectionCancellationAndCancelledContext(t *testing.T) {
	for _, action := range []domain.Action{domain.Reject, domain.Cancel} {
		t.Run(string(action), func(t *testing.T) {
			e, q := fixture(t)
			ctx := salesContext()
			q, err := e.Execute(ctx, q.ID, 1, domain.Submit)
			if err != nil {
				t.Fatal(err)
			}
			actor := ctx
			if action == domain.Reject {
				actor = auth.WithPrincipal(ctx, auth.Principal{ID: "manager", Role: auth.Approver})
			}
			q, err = e.Execute(actor, q.ID, q.Version, action)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.Execute(ctx, q.ID, q.Version, domain.Cancel); !errors.Is(err, domain.ErrTransition) {
				t.Fatalf("terminal mutation: %v", err)
			}
		})
	}
	e, q := fixture(t)
	ctx, cancel := context.WithCancel(salesContext())
	cancel()
	if _, err := e.Execute(ctx, q.ID, q.Version, domain.Submit); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled command: %v", err)
	}
	history, err := e.History(salesContext(), q.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("cancelled command changed history: %+v %v", history, err)
	}
}
