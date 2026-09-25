//go:build integration

package ratelimit

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestRealRedis(t *testing.T) {
	opts, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatalf("TEST_REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	b := make([]byte, 6)
	rand.Read(b)
	l := New(rdb, 5, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	l.Prefix = "fs:test:" + hex.EncodeToString(b) + ":"
	for i := 1; i <= 6; i++ {
		r := l.Allow(ctx, "sdk:a")
		if !r.Known || r.Allowed != (i <= 5) {
			t.Fatalf("request %d = %+v", i, r)
		}
	}
	keys, _ := rdb.Keys(ctx, l.Prefix+"*").Result()
	if len(keys) != 1 {
		t.Fatalf("keys = %v", keys)
	}
	if ttl := rdb.TTL(ctx, keys[0]).Val(); ttl <= 0 || ttl > window+10e9 {
		t.Errorf("TTL = %v", ttl)
	}
	rdb.Del(ctx, keys...)
}
