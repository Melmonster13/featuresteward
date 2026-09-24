package idemtest

import (
	"testing"
	"time"

	"github.com/Melmonster13/featuresteward/internal/idempotency"
)

func TestMemoryContract(t *testing.T) {
	RunContract(t, func(_ *testing.T, ttl, stale time.Duration) idempotency.Store {
		m := NewMemory()
		m.TTL, m.Stale = ttl, stale
		return m
	})
}
