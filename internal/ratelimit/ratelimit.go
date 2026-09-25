// Package ratelimit counts requests per client in fixed one-minute
// windows in Redis. If Redis is unavailable, requests are allowed.
package ratelimit

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Melmonster13/featuresteward/internal/redisguard"
)

const window = time.Minute

// Result is one request's outcome. When Known is false, Redis couldn't be
// asked and the request is allowed without counting.
type Result struct {
	Allowed   bool
	Known     bool
	Limit     int
	Remaining int
	// Reset is how long until the current window ends.
	Reset time.Duration
}

type Limiter struct {
	rdb   redis.UniversalClient
	limit int
	// Prefix namespaces keys.
	Prefix string
	Guard  *redisguard.Guard
	// now is the clock; tests replace it.
	now func() time.Time
}

// New allows limit requests per client per minute.
func New(rdb redis.UniversalClient, limit int, log *slog.Logger) *Limiter {
	return &Limiter{
		rdb: rdb, limit: limit, Prefix: "fs:rate:v1:",
		Guard: redisguard.New("evaluations aren't rate limited", log),
		now:   time.Now,
	}
}

// Allow counts a request from client and says whether it's within the limit.
func (l *Limiter) Allow(ctx context.Context, client string) Result {
	if !l.Guard.Up() {
		return Result{Allowed: true}
	}
	now := l.now()
	start := now.Truncate(window)
	key := l.Prefix + client + ":" + strconv.FormatInt(start.Unix(), 10)

	pipe := l.rdb.TxPipeline()
	incr := pipe.Incr(ctx, key)
	// A little past the window, so a slow clock elsewhere can't reset it early.
	pipe.Expire(ctx, key, window+10*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		l.Guard.Failed(ctx, "count", err)
		return Result{Allowed: true}
	}
	count := int(incr.Val())
	return Result{
		Allowed:   count <= l.limit,
		Known:     true,
		Limit:     l.limit,
		Remaining: max(0, l.limit-count),
		Reset:     start.Add(window).Sub(now),
	}
}
