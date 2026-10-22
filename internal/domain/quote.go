package domain

import (
	"errors"
	"time"
)

var (
	ErrNotFound   = errors.New("quote not found")
	ErrConflict   = errors.New("quote version conflict")
	ErrInvalid    = errors.New("invalid input")
	ErrTransition = errors.New("transition not allowed")
)

type State string

const (
	Draft     State = "draft"
	Submitted State = "submitted"
	Approved  State = "approved"
	Rejected  State = "rejected"
	Issued    State = "issued"
	Cancelled State = "cancelled"
)

// Quote stores monetary values in minor currency units, avoiding floating-point rounding.
type Quote struct {
	ID          string    `json:"id"`
	Reference   string    `json:"reference"`
	Currency    string    `json:"currency"`
	AmountMinor int64     `json:"amount_minor"`
	State       State     `json:"state"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Event struct {
	QuoteID string    `json:"quote_id"`
	Version int64     `json:"version"`
	From    State     `json:"from"`
	To      State     `json:"to"`
	Actor   string    `json:"actor"`
	At      time.Time `json:"at"`
}
