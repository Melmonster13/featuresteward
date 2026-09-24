// Package idemtest provides an in-memory idempotency.Store and a
// contract test suite that every implementation must pass.
package idemtest

import (
	"bytes"
	"context"
	"maps"
	"sync"
	"time"

	"github.com/Melmonster13/featuresteward/internal/idempotency"
)

type entry struct {
	rec     idempotency.Record
	created time.Time
}

type Memory struct {
	mu      sync.Mutex
	entries map[[2]string]*entry
	TTL     time.Duration
	Stale   time.Duration
}

var _ idempotency.Store = (*Memory)(nil)

func NewMemory() *Memory {
	return &Memory{entries: map[[2]string]*entry{}, TTL: idempotency.TTL, Stale: idempotency.StaleAfter}
}

func (m *Memory) Begin(_ context.Context, user, key string, requestHash []byte) (*idempotency.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{user, key}
	now := time.Now()
	if e, ok := m.entries[k]; ok {
		age := now.Sub(e.created)
		if age < m.TTL && (e.rec.Status != 0 || age < m.Stale) {
			rec := e.rec
			rec.Headers = maps.Clone(rec.Headers)
			rec.Body = bytes.Clone(rec.Body)
			return &rec, nil
		}
	}
	m.entries[k] = &entry{rec: idempotency.Record{RequestHash: bytes.Clone(requestHash)}, created: now}
	return nil, nil
}

func (m *Memory) Complete(_ context.Context, user, key string, status int, headers map[string]string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[[2]string{user, key}]; ok {
		e.rec.Status, e.rec.Headers, e.rec.Body = status, maps.Clone(headers), bytes.Clone(body)
	}
	return nil
}

func (m *Memory) Release(_ context.Context, user, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{user, key}
	if e, ok := m.entries[k]; ok && e.rec.Status == 0 {
		delete(m.entries, k)
	}
	return nil
}

func (m *Memory) DeleteExpired(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, e := range m.entries {
		if time.Since(e.created) >= m.TTL {
			delete(m.entries, k)
		}
	}
	return nil
}
