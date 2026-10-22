package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
)

// Repository must commit the quote and event together. Update uses compare-and-swap.
type Repository interface {
	Create(context.Context, domain.Quote, domain.Event) error
	Get(context.Context, string) (domain.Quote, error)
	Update(context.Context, domain.Quote, int64, domain.Event) error
	History(context.Context, string) ([]domain.Event, error)
}

// Step validates or computes against a snapshot before persistence. It must not
// perform irreversible external writes: a concurrent update can still fail.
type Step func(context.Context, domain.Quote) error

type Transition struct{ From, To domain.State }

type Engine struct {
	repo  Repository
	steps map[Transition][]Step
}

func New(repo Repository) *Engine {
	return &Engine{repo: repo, steps: map[Transition][]Step{
		{From: domain.Draft, To: domain.Submitted}:     nil,
		{From: domain.Draft, To: domain.Cancelled}:     nil,
		{From: domain.Submitted, To: domain.Approved}:  nil,
		{From: domain.Submitted, To: domain.Rejected}:  nil,
		{From: domain.Submitted, To: domain.Cancelled}: nil,
		{From: domain.Approved, To: domain.Issued}:     nil,
		{From: domain.Approved, To: domain.Cancelled}:  nil,
	}}
}

// AddStep appends a business rule to an existing transition; configure before serving requests.
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
	Actor       string `json:"actor"`
}

func validText(s string, max int) bool { return strings.TrimSpace(s) != "" && len(s) <= max }

func (e *Engine) Create(ctx context.Context, in CreateInput) (domain.Quote, error) {
	if !validText(in.Reference, 200) || !validText(in.Actor, 200) || in.AmountMinor < 0 || len(in.Currency) != 3 {
		return domain.Quote{}, domain.ErrInvalid
	}
	for _, c := range in.Currency {
		if c < 'A' || c > 'Z' {
			return domain.Quote{}, domain.ErrInvalid
		}
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return domain.Quote{}, fmt.Errorf("generate quote ID: %w", err)
	}
	now := time.Now().UTC()
	q := domain.Quote{ID: hex.EncodeToString(id[:]), Reference: in.Reference, Currency: in.Currency, AmountMinor: in.AmountMinor, State: domain.Draft, Version: 1, CreatedAt: now, UpdatedAt: now}
	err := e.repo.Create(ctx, q, domain.Event{QuoteID: q.ID, Version: q.Version, To: q.State, Actor: in.Actor, At: now})
	if err != nil {
		return domain.Quote{}, fmt.Errorf("create quote: %w", err)
	}
	return q, nil
}

func (e *Engine) Get(ctx context.Context, id string) (domain.Quote, error) {
	return e.repo.Get(ctx, id)
}
func (e *Engine) History(ctx context.Context, id string) ([]domain.Event, error) {
	return e.repo.History(ctx, id)
}

func (e *Engine) Move(ctx context.Context, id string, version int64, to domain.State, actor string) (domain.Quote, error) {
	if version < 1 || !validText(actor, 200) {
		return domain.Quote{}, domain.ErrInvalid
	}
	q, err := e.repo.Get(ctx, id)
	if err != nil {
		return domain.Quote{}, err
	}
	if q.Version != version {
		return domain.Quote{}, domain.ErrConflict
	}
	steps, ok := e.steps[Transition{From: q.State, To: to}]
	if !ok {
		return domain.Quote{}, domain.ErrTransition
	}
	for i, step := range steps {
		if err := step(ctx, q); err != nil {
			return domain.Quote{}, fmt.Errorf("transition step %d: %w", i+1, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return domain.Quote{}, err
	}
	from := q.State
	q.State, q.Version, q.UpdatedAt = to, q.Version+1, time.Now().UTC()
	err = e.repo.Update(ctx, q, version, domain.Event{QuoteID: id, Version: q.Version, From: from, To: to, Actor: actor, At: q.UpdatedAt})
	if err != nil {
		return domain.Quote{}, fmt.Errorf("persist transition: %w", err)
	}
	return q, nil
}
