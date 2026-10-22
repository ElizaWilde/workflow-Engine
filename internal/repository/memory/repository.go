package memory

import (
	"context"
	"sync"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
)

type Repository struct {
	mu     sync.RWMutex
	quotes map[string]domain.Quote
	events map[string][]domain.Event
}

func New() *Repository {
	return &Repository{quotes: make(map[string]domain.Quote), events: make(map[string][]domain.Event)}
}

func (r *Repository) Create(ctx context.Context, q domain.Quote, ev domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := r.quotes[q.ID]; ok {
		return domain.ErrConflict
	}
	r.quotes[q.ID], r.events[q.ID] = q, []domain.Event{ev}
	return nil
}

func (r *Repository) Get(ctx context.Context, id string) (domain.Quote, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return domain.Quote{}, err
	}
	q, ok := r.quotes[id]
	if !ok {
		return domain.Quote{}, domain.ErrNotFound
	}
	return q, nil
}

func (r *Repository) Update(ctx context.Context, q domain.Quote, expected int64, ev domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	old, ok := r.quotes[q.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if old.Version != expected {
		return domain.ErrConflict
	}
	r.quotes[q.ID] = q
	r.events[q.ID] = append(r.events[q.ID], ev)
	return nil
}

func (r *Repository) History(ctx context.Context, id string) ([]domain.Event, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := r.quotes[id]; !ok {
		return nil, domain.ErrNotFound
	}
	return append([]domain.Event(nil), r.events[id]...), nil
}
