package ratelimit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()

func newLimiter(t *testing.T, limit int) (*Limiter, *miniredis.Miniredis, *time.Time, *bytes.Buffer) {
	m := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: m.Addr(), MaxRetries: -1, DialerRetries: 1, DialTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { rdb.Close() })
	var logs bytes.Buffer
	l := New(rdb, limit, slog.New(slog.NewTextHandler(&logs, nil)))
	now := time.Date(2026, 9, 25, 12, 0, 20, 0, time.UTC) // 20s into a window
	l.now = func() time.Time { return now }
	return l, m, &now, &logs
}

func TestLimitsPerWindow(t *testing.T) {
	l, m, now, _ := newLimiter(t, 3)
	for i := 1; i <= 3; i++ {
		r := l.Allow(ctx, "sdk:a")
		if !r.Allowed || !r.Known || r.Limit != 3 || r.Remaining != 3-i || r.Reset != 40*time.Second {
			t.Fatalf("request %d = %+v", i, r)
		}
	}
	if r := l.Allow(ctx, "sdk:a"); r.Allowed || r.Remaining != 0 {
		t.Fatalf("over the limit = %+v", r)
	}
	// Clients are counted separately.
	if r := l.Allow(ctx, "user:sam"); !r.Allowed || r.Remaining != 2 {
		t.Errorf("other client = %+v", r)
	}
	// Keys expire after their window.
	if ttl := m.TTL("fs:rate:v1:sdk:a:" + "1790337600"); ttl != 70*time.Second {
		t.Errorf("TTL = %v; keys %v", ttl, m.Keys())
	}
	// The next window starts fresh.
	*now = now.Add(41 * time.Second)
	if r := l.Allow(ctx, "sdk:a"); !r.Allowed || r.Remaining != 2 || r.Reset != 59*time.Second {
		t.Errorf("next window = %+v", r)
	}
}

func TestRedisDownAllowsRequests(t *testing.T) {
	l, m, _, logs := newLimiter(t, 1)
	m.Close()
	start := time.Now()
	for range 50 {
		if r := l.Allow(ctx, "sdk:a"); !r.Allowed || r.Known {
			t.Fatalf("with Redis down = %+v", r)
		}
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("50 requests with Redis down took %v", d)
	}
	if n := strings.Count(logs.String(), "evaluations aren't rate limited"); n != 1 {
		t.Errorf("logged %d warnings, want 1:\n%s", n, logs.String())
	}
}
