package cache

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/auth/authtest"
	"github.com/Melmonster13/featuresteward/internal/errs"
)

// countingAuth counts SDK key lookups that reach the database.
type countingAuth struct {
	*authtest.Memory
	reads atomic.Int64
}

func (c *countingAuth) AuthenticateSDKKey(ctx context.Context, hash []byte) (string, error) {
	c.reads.Add(1)
	return c.Memory.AuthenticateSDKKey(ctx, hash)
}

type authFixture struct {
	*AuthStore
	inner *countingAuth
	redis *miniredis.Miniredis
	logs  *bytes.Buffer
}

func newAuthFixture(t *testing.T) authFixture {
	m := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: m.Addr(), MaxRetries: -1, DialerRetries: 1, DialTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { rdb.Close() })
	var logs bytes.Buffer
	inner := &countingAuth{Memory: authtest.NewMemory()}
	s := NewAuth(inner, rdb, 30*time.Second, slog.New(slog.NewTextHandler(&logs, nil)))
	s.RecheckAfter = 20 * time.Millisecond
	return authFixture{s, inner, m, &logs}
}

func TestAuthContract(t *testing.T) {
	authtest.RunContract(t, func(t *testing.T) auth.Store { return newAuthFixture(t).AuthStore })
}

func (f authFixture) newKey(t *testing.T, env string) (secret string, id int64) {
	t.Helper()
	secret, hash, prefix := auth.NewSecret(auth.SDKKeyPrefix)
	k, err := f.CreateSDKKey(ctx, "mel", env, "app", hash, prefix)
	if err != nil {
		t.Fatal(err)
	}
	return secret, k.ID
}

func (f authFixture) authn(secret string) (string, error) {
	return f.AuthenticateSDKKey(ctx, auth.HashSecret(secret))
}

func TestSDKKeyLookupsAreCached(t *testing.T) {
	f := newAuthFixture(t)
	secret, _ := f.newKey(t, "prod")
	for range 3 {
		if env, err := f.authn(secret); err != nil || env != "prod" {
			t.Fatalf("authenticate = %q, %v", env, err)
		}
	}
	if n := f.inner.reads.Load(); n != 1 {
		t.Errorf("database lookups = %d, want 1", n)
	}
	// The key in Redis is the secret's hash, never the secret.
	for _, k := range f.redis.Keys() {
		if strings.Contains(k, secret) || strings.Contains(k, secret[len(auth.SDKKeyPrefix):]) {
			t.Errorf("Redis key %q contains the secret", k)
		}
	}
}

func TestBadKeysAreNotCached(t *testing.T) {
	f := newAuthFixture(t)
	for i := range 20 {
		if _, err := f.authn("fs_sdk_nope" + strings.Repeat("0", i)); !errors.Is(err, errs.ErrUnauthorized) {
			t.Fatal(err)
		}
	}
	if keys := f.redis.Keys(); len(keys) != 0 {
		t.Errorf("failed lookups filled Redis: %v", keys)
	}
}

func TestRevokingStopsTheKeyAtOnce(t *testing.T) {
	f := newAuthFixture(t)
	revoked, id := f.newKey(t, "prod")
	kept, _ := f.newKey(t, "dev")
	f.authn(revoked)
	f.authn(kept)
	if err := f.RevokeSDKKey(ctx, "mel", id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.authn(revoked); !errors.Is(err, errs.ErrUnauthorized) {
		t.Fatalf("revoked key still works: %v", err)
	}
	if env, err := f.authn(kept); err != nil || env != "dev" {
		t.Errorf("other key = %q, %v", env, err)
	}

	// An authentication that started before the revoke and caches the
	// key afterwards is caught by the second clear.
	racing, id2 := f.newKey(t, "prod")
	f.RevokeSDKKey(ctx, "mel", id2)
	f.redis.Set(f.Prefix+hexHash(racing), "prod")
	time.Sleep(60 * time.Millisecond)
	if _, err := f.authn(racing); !errors.Is(err, errs.ErrUnauthorized) {
		t.Errorf("racing entry survived: %v", err)
	}
}

func TestSDKKeysWithRedisDown(t *testing.T) {
	f := newAuthFixture(t)
	secret, id := f.newKey(t, "prod")
	f.authn(secret)
	f.redis.Close()
	start := time.Now()
	for range 50 {
		if env, err := f.authn(secret); err != nil || env != "prod" {
			t.Fatalf("with Redis down = %q, %v", env, err)
		}
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("50 lookups took %v", d)
	}
	// Revoking still works; the database is the source of truth.
	f.RevokeSDKKey(ctx, "mel", id)
	if _, err := f.authn(secret); !errors.Is(err, errs.ErrUnauthorized) {
		t.Errorf("revoked while Redis is down: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // the second clear fails too

	// Redis comes back with the old entry still in it. The revoke
	// couldn't clear it, so the cache is cleared before it's used again.
	if err := f.redis.Restart(); err != nil {
		t.Fatal(err)
	}
	if !f.redis.Exists(f.Prefix + hexHash(secret)) {
		t.Fatal("test setup: the old entry should have survived the outage")
	}
	f.Guard.Reset()
	if _, err := f.authn(secret); !errors.Is(err, errs.ErrUnauthorized) {
		t.Errorf("revoked key works again after Redis came back: %v", err)
	}
	if f.redis.Exists(f.Prefix + hexHash(secret)) {
		t.Error("stale entry not cleared")
	}
}

func hexHash(secret string) string { return hex.EncodeToString(auth.HashSecret(secret)) }
