package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestConnectRedis(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	if c, err := connectRedis(ctx, "", log); c != nil || err != nil || !strings.Contains(logs.String(), "REDIS_URL is not set") {
		t.Errorf("unset: %v, %v, logs %q", c, err, logs.String())
	}

	m := miniredis.RunT(t)
	c, err := connectRedis(ctx, "redis://"+m.Addr()+"/0", log)
	if err != nil || c == nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	if err := c.Set(ctx, "k", "v", 0).Err(); err != nil || m.Exists("k") == false {
		t.Errorf("set through client: %v", err)
	}

	// A bad URL is an error that doesn't repeat the (possibly secret) URL.
	_, err = connectRedis(ctx, "http://user:hunter2@example.com", log)
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("bad URL error = %v", err)
	}

	// Redis being down isn't fatal.
	logs.Reset()
	addr := m.Addr()
	m.Close()
	down, err := connectRedis(ctx, "redis://"+addr+"/0", log)
	if err != nil || down == nil || !strings.Contains(logs.String(), "Redis is unavailable") {
		t.Errorf("down: %v, %v, logs %q", down, err, logs.String())
	}
	down.Close()
}

func TestSecondsEnv(t *testing.T) {
	for v, want := range map[string]time.Duration{"": 30 * time.Second, "0": 0, "5": 5 * time.Second} {
		t.Setenv("TEST_SECONDS", v)
		if got, err := secondsEnv("TEST_SECONDS", 30); err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", v, got, err, want)
		}
	}
	for _, v := range []string{"-1", "1.5", "30s", "x"} {
		t.Setenv("TEST_SECONDS", v)
		if _, err := secondsEnv("TEST_SECONDS", 30); err == nil || !strings.Contains(err.Error(), "TEST_SECONDS must be a whole number of seconds") {
			t.Errorf("%q: err = %v", v, err)
		}
	}
}
