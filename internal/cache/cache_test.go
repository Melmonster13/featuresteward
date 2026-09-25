package cache

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Melmonster13/featuresteward/internal/errs"
	"github.com/Melmonster13/featuresteward/internal/eval"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
)

// counting counts EvalConfig calls that reach the database.
type counting struct {
	*flagtest.Memory
	reads atomic.Int64
}

func (c *counting) EvalConfig(ctx context.Context, key, env string) (eval.Flag, error) {
	c.reads.Add(1)
	return c.Memory.EvalConfig(ctx, key, env)
}

type fixture struct {
	*Store
	inner *counting
	redis *miniredis.Miniredis
	logs  *bytes.Buffer
}

func newFixture(t *testing.T) fixture {
	m := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: m.Addr(), MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { rdb.Close() })
	var logs bytes.Buffer
	inner := &counting{Memory: flagtest.NewMemory()}
	s := New(inner, rdb, 30*time.Second, slog.New(slog.NewTextHandler(&logs, nil)))
	s.RecheckAfter = 20 * time.Millisecond
	return fixture{s, inner, m, &logs}
}

func TestContract(t *testing.T) {
	flagtest.RunContract(t, func(t *testing.T) flag.Store { return newFixture(t).Store })
}

var ctx = context.Background()

func (f fixture) eval(t *testing.T, key, env string) eval.Flag {
	t.Helper()
	got, err := f.EvalConfig(ctx, key, env)
	if err != nil {
		t.Fatalf("EvalConfig(%s, %s): %v", key, env, err)
	}
	return got
}

func (f fixture) wantReads(t *testing.T, n int64) {
	t.Helper()
	if got := f.inner.reads.Load(); got != n {
		t.Fatalf("database reads = %d, want %d", got, n)
	}
}

func TestHitsSkipTheDatabase(t *testing.T) {
	f := newFixture(t)
	f.CreateFlag(ctx, "mel", "new-checkout", "New checkout", "", "")
	f.UpdateEnvironment(ctx, "mel", "new-checkout", "prod", flag.EnvConfig{Enabled: true, RolloutPercentage: 25,
		Rules: []eval.Rule{{Attribute: eval.AttributeGroup, Values: []string{"staff"}, Serve: true}}}, "x")
	first := f.eval(t, "new-checkout", "prod")
	second := f.eval(t, "new-checkout", "prod")
	f.wantReads(t, 1)
	if !second.Enabled || second.RolloutPercentage != 25 || second.Key != "new-checkout" || second.Rules[0].Values[0] != "staff" {
		t.Errorf("cached = %+v, first = %+v", second, first)
	}
	if ttl := f.redis.TTL("fs:eval:v1:prod:new-checkout"); ttl != 30*time.Second {
		t.Errorf("TTL = %v", ttl)
	}
	// Environments are cached separately.
	f.eval(t, "new-checkout", "dev")
	f.wantReads(t, 2)

	f.redis.FastForward(31 * time.Second)
	f.eval(t, "new-checkout", "prod")
	f.wantReads(t, 3)
}

func TestUnknownFlagsAreCached(t *testing.T) {
	f := newFixture(t)
	for range 3 {
		if _, err := f.EvalConfig(ctx, "nope", "prod"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("unknown flag: %v", err)
		}
	}
	f.wantReads(t, 1)
	// Creating the flag clears the "missing" entry.
	f.CreateFlag(ctx, "mel", "nope", "Now it exists", "", "")
	f.eval(t, "nope", "prod")
	f.wantReads(t, 2)
}

func TestChangesClearTheCache(t *testing.T) {
	f := newFixture(t)
	f.CreateFlag(ctx, "mel", "new-checkout", "New checkout", "", "")
	on := flag.EnvConfig{Enabled: true, RolloutPercentage: 100}

	// The kill switch is visible on the next evaluation.
	f.UpdateEnvironment(ctx, "mel", "new-checkout", "prod", on, "x")
	if !f.eval(t, "new-checkout", "prod").Enabled {
		t.Fatal("not on")
	}
	f.UpdateEnvironment(ctx, "sam", "new-checkout", "prod", flag.EnvConfig{RolloutPercentage: 100}, "")
	if f.eval(t, "new-checkout", "prod").Enabled {
		t.Error("kill switch hidden by the cache")
	}

	// Approving a request.
	r, err := f.CreateChangeRequest(ctx, "sam", "new-checkout", "prod", on, "", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.eval(t, "new-checkout", "prod")
	if _, err := f.ApproveChangeRequest(ctx, "ana", r.ID, ""); err != nil {
		t.Fatal(err)
	}
	if !f.eval(t, "new-checkout", "prod").Enabled {
		t.Error("approved change hidden by the cache")
	}

	// Archiving, in every environment.
	f.eval(t, "new-checkout", "dev")
	f.ArchiveFlag(ctx, "mel", "new-checkout")
	for _, env := range []string{"dev", "prod"} {
		if _, err := f.EvalConfig(ctx, "new-checkout", env); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("archived flag in %s: %v", env, err)
		}
	}
}

func TestNewEnvironmentsClearMissingEntries(t *testing.T) {
	f := newFixture(t)
	f.CreateFlag(ctx, "mel", "new-checkout", "New checkout", "", "")
	f.CreateFlag(ctx, "mel", "dark-mode", "Dark mode", "", "")
	for _, key := range []string{"new-checkout", "dark-mode"} {
		if _, err := f.EvalConfig(ctx, key, "qa"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("before qa exists: %v", err)
		}
	}
	f.eval(t, "new-checkout", "prod") // unrelated entry stays
	if _, err := f.CreateEnvironment(ctx, "mel", flag.Environment{Key: "qa", Name: "QA"}); err != nil {
		t.Fatal(err)
	}
	f.eval(t, "new-checkout", "qa")
	f.eval(t, "dark-mode", "qa")
	if !f.redis.Exists("fs:eval:v1:prod:new-checkout") {
		t.Error("creating qa cleared prod's entry")
	}
}

func TestSecondClearCatchesARacingRead(t *testing.T) {
	f := newFixture(t)
	f.CreateFlag(ctx, "mel", "new-checkout", "New checkout", "", "")
	f.UpdateEnvironment(ctx, "sam", "new-checkout", "dev", flag.EnvConfig{Enabled: true, RolloutPercentage: 100}, "")
	// A read that started before the change stores the old value after
	// the first clear.
	f.redis.Set("fs:eval:v1:dev:new-checkout", `{"Key":"new-checkout","Enabled":false}`)
	time.Sleep(60 * time.Millisecond)
	if f.redis.Exists("fs:eval:v1:dev:new-checkout") {
		t.Fatal("stale entry survived the second clear")
	}
	if !f.eval(t, "new-checkout", "dev").Enabled {
		t.Error("stale value served")
	}
}

func TestRedisFailuresFallBackToTheDatabase(t *testing.T) {
	f := newFixture(t)
	f.CreateFlag(ctx, "mel", "new-checkout", "New checkout", "", "")
	f.redis.Set("fs:eval:v1:dev:new-checkout", "{not json")
	if f.eval(t, "new-checkout", "dev").Key != "new-checkout" {
		t.Error("corrupt entry not treated as a miss")
	}

	addr := f.redis.Addr()
	f.redis.Close()
	// Only the first read waits for Redis; the rest skip it.
	start := time.Now()
	for range 50 {
		f.eval(t, "new-checkout", "prod")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("50 reads with Redis down took %v", d)
	}
	f.UpdateEnvironment(ctx, "sam", "new-checkout", "dev", flag.EnvConfig{Enabled: true, RolloutPercentage: 100}, "")
	if !f.eval(t, "new-checkout", "dev").Enabled {
		t.Error("stale value while Redis is down")
	}
	if n := strings.Count(f.logs.String(), "evaluation cache unavailable"); n != 1 {
		t.Errorf("logged %d warnings, want 1 (throttled):\n%s", n, f.logs.String())
	}

	// After the cooldown, the cache is used again once Redis is back.
	if err := f.redis.StartAddr(addr); err != nil {
		t.Fatal(err)
	}
	f.downUntil.Store(0)
	f.inner.reads.Store(0)
	f.eval(t, "new-checkout", "prod")
	f.eval(t, "new-checkout", "prod")
	f.wantReads(t, 1)
}

func TestCallerCancellationDoesntTripTheBreaker(t *testing.T) {
	f := newFixture(t)
	f.CreateFlag(ctx, "mel", "new-checkout", "New checkout", "", "")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	f.EvalConfig(cancelled, "new-checkout", "prod")
	if f.downUntil.Load() != 0 {
		t.Error("a cancelled request turned the cache off")
	}
}
