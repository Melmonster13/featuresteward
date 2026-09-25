// Package redisguard helps code that treats Redis as optional: after a
// failure it skips Redis for a cooldown, so an outage doesn't add a
// timeout to every request, and it logs failures at most once a minute.
package redisguard

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

type Guard struct {
	// What the caller does without Redis, for the log message.
	Fallback string
	Cooldown time.Duration
	Log      *slog.Logger

	downUntil atomic.Int64 // unix nanos
	lastWarn  atomic.Int64 // unix nanos
}

func New(fallback string, log *slog.Logger) *Guard {
	return &Guard{Fallback: fallback, Cooldown: 5 * time.Second, Log: log}
}

// Up reports whether to try Redis.
func (g *Guard) Up() bool { return time.Now().UnixNano() >= g.downUntil.Load() }

// Failed starts a cooldown and logs, unless the caller gave up: then the
// error is theirs, not Redis's.
func (g *Guard) Failed(ctx context.Context, op string, err error) {
	if ctx.Err() != nil {
		return
	}
	g.downUntil.Store(time.Now().Add(g.Cooldown).UnixNano())
	g.Warn(ctx, op, err)
}

// Warn logs a Redis failure without starting a cooldown.
func (g *Guard) Warn(ctx context.Context, op string, err error) {
	now := time.Now().UnixNano()
	last := g.lastWarn.Load()
	if now-last < int64(time.Minute) || !g.lastWarn.CompareAndSwap(last, now) {
		return
	}
	g.Log.WarnContext(ctx, "Redis unavailable; "+g.Fallback, "op", op, "err", err)
}

// Reset ends a cooldown; for tests.
func (g *Guard) Reset() { g.downUntil.Store(0) }
