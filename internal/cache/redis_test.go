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

	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
)

// TestRealRedis runs the store contract through a real Redis. Each test
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
	flagtest.RunContract(t, func(t *testing.T) flag.Store {
		b := make([]byte, 6)
		rand.Read(b)
		s := New(flagtest.NewMemory(), rdb, time.Minute, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
		s.Prefix = "fs:test:" + hex.EncodeToString(b) + ":"
		s.RecheckAfter = 10 * time.Millisecond
		t.Cleanup(func() {
			time.Sleep(20 * time.Millisecond) // let second clears finish
			ctx := context.Background()
			iter := rdb.Scan(ctx, 0, s.Prefix+"*", 500).Iterator()
			for iter.Next(ctx) {
				rdb.Del(ctx, iter.Val())
			}
		})
		return s
	})
}
