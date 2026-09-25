package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/auth/authtest"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
	"github.com/Melmonster13/featuresteward/internal/idempotency/idemtest"
	"github.com/Melmonster13/featuresteward/internal/ratelimit"
)

// fakeLimiter allows limit requests per client and records who asked.
type fakeLimiter struct {
	mu      sync.Mutex
	limit   int
	counts  map[string]int
	unknown bool
}

func (f *fakeLimiter) Allow(_ context.Context, client string) ratelimit.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unknown {
		return ratelimit.Result{Allowed: true}
	}
	f.counts[client]++
	n := f.counts[client]
	return ratelimit.Result{Allowed: n <= f.limit, Known: true, Limit: f.limit, Remaining: max(0, f.limit-n), Reset: 12400 * time.Millisecond}
}

func limitedClient(t *testing.T, limit int) (*client, *fakeLimiter) {
	lim := &fakeLimiter{limit: limit, counts: map[string]int{}}
	flags, users, idem := flagtest.NewMemory(), authtest.NewMemory(), idemtest.NewMemory()
	c := &client{t: t, users: users, idem: idem,
		h: NewRouter(flags, users, idem, slog.New(slog.NewTextHandler(io.Discard, nil)), WithRateLimiter(lim))}
	c.token = c.newUser("mel", auth.RoleAdmin)
	c.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout"}`, 201)
	return c, lim
}

func evaluate(c *client) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/evaluate", strings.NewReader(`{"flag":"new-checkout","environment":"prod","user_id":"u1"}`))
	if c.session != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.session})
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	} else {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func TestEvaluateIsRateLimited(t *testing.T) {
	c, lim := limitedClient(t, 2)
	for i := range 2 {
		rec := evaluate(c)
		h := rec.Header()
		if rec.Code != 200 || h.Get("RateLimit-Limit") != "2" || h.Get("RateLimit-Remaining") != []string{"1", "0"}[i] ||
			h.Get("RateLimit-Reset") != "13" || h.Get("Retry-After") != "" {
			t.Fatalf("request %d = %d %v", i+1, rec.Code, h)
		}
	}
	rec := evaluate(c)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "13" || !strings.Contains(rec.Body.String(), "2 evaluations per minute") {
		t.Fatalf("over the limit = %d %v %s", rec.Code, rec.Header(), rec.Body)
	}

	// Other routes aren't limited.
	for range 5 {
		c.mustDo("GET", "/api/v1/flags", "", 200)
	}
	// SDK keys are counted separately from users, and from each other,
	// by hash rather than secret.
	k1, k2 := c.newSDKKey("prod"), c.newSDKKey("prod")
	if evaluate(c.as(k1)).Code != 200 || evaluate(c.as(k2)).Code != 200 {
		t.Error("SDK keys share the user's count")
	}
	for id := range lim.counts {
		if strings.HasPrefix(id, "sdk:") && (len(id) != len("sdk:")+24 || strings.Contains(k1, id[4:])) {
			t.Errorf("SDK client id %q", id)
		}
	}
	// A user's tokens and sessions share one count.
	s := c.login()
	if rec := evaluate(s); rec.Code != 429 {
		t.Errorf("session after the token hit the limit = %d", rec.Code)
	}
	if lim.counts["user:mel"] != 4 {
		t.Errorf("user:mel count = %d, want 4", lim.counts["user:mel"])
	}
}

func TestNoRateLimitHeadersWhenRedisIsDown(t *testing.T) {
	c, lim := limitedClient(t, 1)
	lim.unknown = true
	for range 3 {
		if rec := evaluate(c); rec.Code != 200 || rec.Header().Get("RateLimit-Limit") != "" {
			t.Fatalf("got %d %v", rec.Code, rec.Header())
		}
	}
}

func TestEvaluationsAreRecorded(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	flags, users, idem := flagtest.NewMemory(), authtest.NewMemory(), idemtest.NewMemory()
	c := &client{t: t, users: users, idem: idem,
		h: NewRouter(flags, users, idem, slog.New(slog.NewTextHandler(io.Discard, nil)),
			WithUsage(func(k, env string) { mu.Lock(); seen = append(seen, k+"/"+env); mu.Unlock() }))}
	c.token = c.newUser("mel", auth.RoleAdmin)
	c.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout"}`, 201)
	evaluate(c)
	c.mustDo("POST", "/api/v1/evaluate", `{"flag":"nope","environment":"prod"}`, 404)
	if len(seen) != 1 || seen[0] != "new-checkout/prod" {
		t.Errorf("seen = %v; unknown flags shouldn't count", seen)
	}
}
