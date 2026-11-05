package memory

import (
	"context"
	"sync"
	"time"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/outbox"
	"github.com/google/uuid"
)

type Repository struct {
	mu       sync.RWMutex
	quotes   map[string]domain.Quote
	events   map[string][]domain.Event
	messages []message
}

type message struct {
	delivery  outbox.Delivery
	available time.Time
	done      bool
}

func (r *Repository) enqueue(ev domain.Event) {
	r.messages = append(r.messages, message{delivery: outbox.Delivery{ID: int64(len(r.messages) + 1), Event: ev}})
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
	r.enqueue(ev)
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
	r.enqueue(ev)
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

func (r *Repository) Claim(ctx context.Context, now time.Time, lease time.Duration) (outbox.Delivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return outbox.Delivery{}, err
	}
	for i := range r.messages {
		m := &r.messages[i]
		if m.done || m.available.After(now) {
			continue
		}
		m.delivery.Attempts++
		m.delivery.Token = uuid.NewString()
		m.available = now.Add(lease)
		return m.delivery, nil
	}
	return outbox.Delivery{}, outbox.ErrEmpty
}

func (r *Repository) delivery(d outbox.Delivery) (*message, error) {
	if d.ID < 1 || d.ID > int64(len(r.messages)) {
		return nil, domain.ErrConflict
	}
	m := &r.messages[d.ID-1]
	if m.done || d.Token == "" || m.delivery.Token != d.Token {
		return nil, domain.ErrConflict
	}
	return m, nil
}

func (r *Repository) Ack(ctx context.Context, d outbox.Delivery) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m, err := r.delivery(d)
	if err != nil {
		return err
	}
	m.done = true
	return nil
}

func (r *Repository) Retry(ctx context.Context, d outbox.Delivery, next time.Time, _ string, dead bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m, err := r.delivery(d)
	if err != nil {
		return err
	}
	m.available, m.done, m.delivery.Token = next, dead, ""
	return nil
}
