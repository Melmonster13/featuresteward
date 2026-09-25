package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// connectRedis returns a client for rawURL, or nil if it's empty: Redis
// is optional. An unreachable Redis is logged, not fatal; the client
// reconnects when it's back.
func connectRedis(ctx context.Context, rawURL string, log *slog.Logger) (*redis.Client, error) {
	if rawURL == "" {
		log.Warn("REDIS_URL is not set; evaluations won't be cached or rate limited")
		return nil, nil
	}
	// Route go-redis's own messages into the server's structured log.
	redis.SetLogger(redisLog{log})
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		// Not wrapped: the URL may contain a password.
		return nil, errors.New("REDIS_URL must be a redis:// or rediss:// URL")
	}
	// Redis is a cache, so fail fast instead of slowing every evaluation.
	opts.DialTimeout = 500 * time.Millisecond
	opts.ReadTimeout = 100 * time.Millisecond
	opts.WriteTimeout = 100 * time.Millisecond
	opts.PoolTimeout = 200 * time.Millisecond
	opts.MaxRetries = 1
	opts.DialerRetries = 1
	c := redis.NewClient(opts)
	if err := c.Ping(ctx).Err(); err != nil {
		log.Warn("Redis is unavailable; continuing without it until it's back", "err", err)
	}
	return c, nil
}

type redisLog struct{ log *slog.Logger }

func (l redisLog) Printf(ctx context.Context, format string, v ...any) {
	l.log.WarnContext(ctx, fmt.Sprintf(format, v...), "component", "redis")
}

// secondsEnv reads a whole number of seconds, 0 or more, from name.
func secondsEnv(name string, fallback int) (time.Duration, error) {
	n, err := intEnv(name, fallback, "seconds")
	return time.Duration(n) * time.Second, err
}

// intEnv reads a whole number, 0 or more, from name.
func intEnv(name string, fallback int, unit string) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a whole number of %s, 0 or more; got %q", name, unit, v)
	}
	return n, nil
}
