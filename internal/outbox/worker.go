// Package outbox delivers committed events with leases and bounded retries.
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
)

var ErrEmpty = errors.New("no event available")

const MaxAttempts = 5
const Lease = 30 * time.Second

type Delivery struct {
	ID       int64
	Token    string
	Event    domain.Event
	Attempts int
}

type Store interface {
	Claim(context.Context, time.Time, time.Duration) (Delivery, error)
	Ack(context.Context, Delivery) error
	Retry(context.Context, Delivery, time.Time, string, bool) error
}

type Handler func(context.Context, domain.Event) error

type Worker struct {
	Store  Store
	Handle Handler
	Logger *slog.Logger
	// Now permits deterministic retry tests; nil uses the system clock.
	Now func() time.Time
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now().UTC()
}

// ProcessOne returns false if no event is due. Delivery is at least once:
// consumers must deduplicate using quote ID and version.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	/*
		→ Store.Claim(...)        obtain a committed event
		→ Handle(ctx, event)      log it or send a webhook
		→ Store.Ack(...)          record success
			or Store.Retry(...)    schedule another attempt
	*/
	d, err := w.Store.Claim(ctx, w.now(), Lease)
	if errors.Is(err, ErrEmpty) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = w.Handle(deliveryCtx, d.Event)
	cancel()
	if err == nil {
		return true, w.Store.Ack(ctx, d)
	}
	delay := time.Second << min(max(d.Attempts-1, 0), 6)
	reason := err.Error()
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	dead := d.Attempts >= MaxAttempts
	if retryErr := w.Store.Retry(ctx, d, w.now().Add(delay), reason, dead); retryErr != nil {
		return true, fmt.Errorf("record delivery failure: %w", retryErr)
	}
	if w.Logger != nil {
		w.Logger.WarnContext(ctx, "event delivery failed", "quote_id", d.Event.QuoteID, "version", d.Event.Version, "attempt", d.Attempts, "dead", dead, "error", err)
	}
	return true, nil
}

func (w *Worker) Run(ctx context.Context) {
	/*
		Worker.Run(ctx)
			→ Worker.ProcessOne(ctx)
	*/
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		worked, err := w.ProcessOne(ctx)
		if err != nil && ctx.Err() == nil && w.Logger != nil {
			w.Logger.ErrorContext(ctx, "outbox processing failed", "error", err)
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
