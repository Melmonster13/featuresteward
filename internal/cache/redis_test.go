//go:build integration

package cache

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/auth/authtest"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
)

// TestRealRedis runs the store contracts through a real Redis. Each test
// gets its own key prefix and removes its keys.
func TestRealRedis(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Fatal("TEST_REDIS_URL is not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	cleanup := func(t *testing.T, prefix string) {
		t.Cleanup(func() {
			time.Sleep(20 * time.Millisecond) // let second clears finish
			ctx := context.Background()
			iter := rdb.Scan(ctx, 0, prefix+"*", 500).Iterator()
			for iter.Next(ctx) {
				rdb.Del(ctx, iter.Val())
			}
		})
	}
	authtest.RunContract(t, func(t *testing.T) auth.Store {
		s := NewAuth(authtest.NewMemory(), rdb, time.Minute, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
		s.Prefix = "fs:test:" + randomHex() + ":"
		s.RecheckAfter = 10 * time.Millisecond
		cleanup(t, s.Prefix)
		return s
	})
	flagtest.RunContract(t, func(t *testing.T) flag.Store {
		s := New(flagtest.NewMemory(), rdb, time.Minute, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
		s.Prefix = "fs:test:" + randomHex() + ":"
		s.RecheckAfter = 10 * time.Millisecond
		cleanup(t, s.Prefix)
		return s
	})
}

func randomHex() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
