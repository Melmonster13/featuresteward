// Package idempotency stores responses to requests sent with an
// Idempotency-Key header so retries can be replayed.
package idempotency

import (
	"context"
	"time"
)

const (
	// TTL is how long a completed response can be replayed.
	TTL = 24 * time.Hour
	// StaleAfter is when an unfinished request is assumed abandoned (for
	// example, the server crashed) and its key can be reused. It must
	// exceed the server's write timeout.
	StaleAfter = time.Minute
)

// Record is a stored request and, once complete, its response.
type Record struct {
	RequestHash []byte
	Status      int // 0 while the first request is still in progress
	Headers     map[string]string
	Body        []byte
}

// Store keeps idempotency records per user and key.
type Store interface {
	// Begin reserves key for a new request and returns nil, or returns
	// the existing record if the key is in use. Records past TTL, and
	// in-progress records past StaleAfter, are replaced.
	Begin(ctx context.Context, user, key string, requestHash []byte) (*Record, error)
	Complete(ctx context.Context, user, key string, status int, headers map[string]string, body []byte) error
	// Release frees an in-progress key so the request can be retried.
	Release(ctx context.Context, user, key string) error
	// DeleteExpired removes records past TTL.
	DeleteExpired(ctx context.Context) error
}
