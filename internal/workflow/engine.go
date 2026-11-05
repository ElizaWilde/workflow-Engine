package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"example.com/oracle-bmi/workflow-engine/internal/auth"
	"example.com/oracle-bmi/workflow-engine/internal/domain"
)

// Repository commits a quote, audit event, and outbox record atomically.
// Update uses compare-and-swap to reject stale commands.
type Repository interface {
	Create(context.Context, domain.Quote, domain.Event) error
	Get(context.Context, string) (domain.Quote, error)
	Update(context.Context, domain.Quote, int64, domain.Event) error
	History(context.Context, string) ([]domain.Event, error)
}

// Step checks a snapshot before persistence. It must not perform external writes:
// a concurrent update can still fail after rules have passed.
type Step func(context.Context, domain.Quote) error
type Transition struct{ From, To domain.State }

type Engine struct {
	repo  Repository
	steps map[Transition][]Step
}

var transitions = map[domain.Action]map[domain.State]domain.State{
	domain.Submit:  {domain.Draft: domain.Submitted},
	domain.Approve: {domain.Submitted: domain.Approved},
	domain.Reject:  {domain.Submitted: domain.Rejected},
	domain.Issue:   {domain.Approved: domain.Issued},
	domain.Cancel:  {domain.Draft: domain.Cancelled, domain.Submitted: domain.Cancelled, domain.Approved: domain.Cancelled},
}

func New(repo Repository) *Engine {
	e := &Engine{repo: repo, steps: make(map[Transition][]Step)}
	for _, states := range transitions {
		for from, to := range states {
			e.steps[Transition{from, to}] = nil
		}
	}
	return e
}

// AddStep configures additional business rules before serving requests.
func (e *Engine) AddStep(t Transition, step Step) error {
	if _, ok := e.steps[t]; !ok || step == nil {
		return domain.ErrInvalid
	}
	e.steps[t] = append(e.steps[t], step)
	return nil
}

type CreateInput struct {
	Reference   string `json:"reference"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

type UpdateInput struct {
	Version     int64  `json:"version"`
	Reference   string `json:"reference"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

func validInput(reference, currency string, amount int64) bool {
	if strings.TrimSpace(reference) == "" || len(reference) > 200 || amount < 0 || len(currency) != 3 {
		return false
	}
	for _, c := range currency {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func principal(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return p, domain.ErrUnauthorized
	}
	return p, nil
}

func canRead(p auth.Principal, q domain.Quote) bool {
	return p.Role == auth.Admin || p.Role == auth.Approver || p.ID == q.Owner
}

func canEdit(p auth.Principal, q domain.Quote) bool {
	return p.Role == auth.Admin || (p.Role == auth.Sales && p.ID == q.Owner)
}

func event(q domain.Quote, from domain.State, action domain.Action, actor string) domain.Event {
	return domain.Event{QuoteID: q.ID, Version: q.Version, From: from, To: q.State, Actor: actor, At: q.UpdatedAt, Action: action, Quote: q}
}


//Validate input and create a draft
func (e *Engine) Create(ctx context.Context, in CreateInput) (domain.Quote, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.Quote{}, err
	}
	if p.Role != auth.Sales && p.Role != auth.Admin {
		return domain.Quote{}, domain.ErrForbidden
	}
	if !validInput(in.Reference, in.Currency, in.AmountMinor) {
		return domain.Quote{}, domain.ErrInvalid
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return domain.Quote{}, fmt.Errorf("generate quote ID: %w", err)
	}
	now := time.Now().UTC()
	q := domain.Quote{ID: hex.EncodeToString(id[:]), Reference: in.Reference, Currency: in.Currency, AmountMinor: in.AmountMinor, Owner: p.ID, State: domain.Draft, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := e.repo.Create(ctx, q, event(q, "", domain.Create, p.ID)); err != nil {
		return domain.Quote{}, fmt.Errorf("create quote: %w", err)
	}
	return q, nil
}

//Retrieve a quote and check read permission
func (e *Engine) Get(ctx context.Context, id string) (domain.Quote, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.Quote{}, err
	}
	q, err := e.repo.Get(ctx, id)
	if err != nil {
		return domain.Quote{}, err
	}
	if !canRead(p, q) {
		return domain.Quote{}, domain.ErrForbidden
	}
	return q, nil
}

//Retrieve the quote’s audit events
func (e *Engine) History(ctx context.Context, id string) ([]domain.Event, error) {
	if _, err := e.Get(ctx, id); err != nil {
		return nil, err
	}
	return e.repo.History(ctx, id)
}

func (e *Engine) current(ctx context.Context, id string, version int64) (domain.Quote, error) {
	if version < 1 || version == math.MaxInt64 {
		return domain.Quote{}, domain.ErrInvalid
	}
	q, err := e.Get(ctx, id)
	if err != nil {
		return domain.Quote{}, err
	}
	if q.Version != version {
		return domain.Quote{}, domain.ErrConflict
	}
	return q, nil
}

//Edit a draft using its current version
func (e *Engine) Update(ctx context.Context, id string, in UpdateInput) (domain.Quote, error) {
	q, err := e.current(ctx, id, in.Version)
	if err != nil {
		return domain.Quote{}, err
	}
	p, _ := principal(ctx)
	if !canEdit(p, q) {
		return domain.Quote{}, domain.ErrForbidden
	}
	if q.State != domain.Draft {
		return domain.Quote{}, domain.ErrTransition
	}
	if !validInput(in.Reference, in.Currency, in.AmountMinor) {
		return domain.Quote{}, domain.ErrInvalid
	}
	q.Reference, q.Currency, q.AmountMinor = in.Reference, in.Currency, in.AmountMinor
	q.Version, q.UpdatedAt = q.Version+1, time.Now().UTC()
	if err := e.repo.Update(ctx, q, in.Version, event(q, q.State, domain.Update, p.ID)); err != nil {
    		return domain.Quote{}, fmt.Errorf("update quote: %w", err)
	}
	return q, nil
}

//Resolve an action, check permissions and rules, then save
func (e *Engine) Execute(ctx context.Context, id string, version int64, action domain.Action) (domain.Quote, error) {
	q, err := e.current(ctx, id, version)
	if err != nil {
		return domain.Quote{}, err
	}
	to, ok := transitions[action][q.State]
	if !ok {
		return domain.Quote{}, domain.ErrTransition
	}
	p, _ := principal(ctx)
	if action == domain.Approve || action == domain.Reject {
		if p.Role != auth.Approver && p.Role != auth.Admin {
			return domain.Quote{}, domain.ErrForbidden
		}
		if p.ID == q.Owner {
			return domain.Quote{}, fmt.Errorf("%w: owners cannot approve or reject their own quote", domain.ErrRule)
		}
	} else if !canEdit(p, q) {
		return domain.Quote{}, domain.ErrForbidden
	}
	if action == domain.Submit && q.AmountMinor == 0 {
		return domain.Quote{}, fmt.Errorf("%w: submission requires a positive price", domain.ErrRule)
	}
	for i, step := range e.steps[Transition{q.State, to}] {
		if err := step(ctx, q); err != nil {
			return domain.Quote{}, fmt.Errorf("transition step %d: %w", i+1, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return domain.Quote{}, err
	}
	from := q.State
	q.State, q.Version, q.UpdatedAt = to, q.Version+1, time.Now().UTC()
	if err := e.repo.Update(ctx, q, version, event(q, from, action, p.ID)); err != nil {
		return domain.Quote{}, fmt.Errorf("persist transition: %w", err)
	}
	return q, nil
}

// Move supports the original target-state API through the same action rules.
func (e *Engine) Move(ctx context.Context, id string, version int64, to domain.State) (domain.Quote, error) {
	actions := map[domain.State]domain.Action{domain.Submitted: domain.Submit, domain.Approved: domain.Approve, domain.Rejected: domain.Reject, domain.Issued: domain.Issue, domain.Cancelled: domain.Cancel}
	action, ok := actions[to]
	if !ok {
		return domain.Quote{}, domain.ErrTransition
	}
	return e.Execute(ctx, id, version, action)
}
