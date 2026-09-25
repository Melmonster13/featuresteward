package cache

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/redisguard"
)

// AuthStore is an auth.Store whose AuthenticateSDKKey reads through Redis.
//
// Only successful lookups are cached, so random keys can't fill Redis.
// Revoking any SDK key clears every cached one, right away and again a
// moment later in case an authentication raced the revoke. Revocations
// are rare and there are only as many entries as active keys.
//
// If Redis can't be cleared during a revoke, this server clears it before
// using it again. Another server sharing the Redis could still accept the
// revoked key until its entry expires: at most the TTL after the revoke.
type AuthStore struct {
	auth.Store
	rdb redis.UniversalClient
	ttl time.Duration

	Prefix       string
	RecheckAfter time.Duration
	Guard        *redisguard.Guard

	// uncleared is set when a revoke couldn't clear Redis.
	uncleared atomic.Bool
}

var _ auth.Store = (*AuthStore)(nil)

func NewAuth(inner auth.Store, rdb redis.UniversalClient, ttl time.Duration, log *slog.Logger) *AuthStore {
	return &AuthStore{Store: inner, rdb: rdb, ttl: ttl, Prefix: "fs:sdk:v1:", RecheckAfter: time.Second,
		Guard: redisguard.New("SDK keys are checked in the database", log)}
}

func (s *AuthStore) AuthenticateSDKKey(ctx context.Context, hash []byte) (string, error) {
	if !s.Guard.Up() {
		return s.Store.AuthenticateSDKKey(ctx, hash)
	}
	if s.uncleared.Load() {
		if err := s.clearAll(ctx); err != nil {
			s.Guard.Failed(ctx, "clear", err)
			return s.Store.AuthenticateSDKKey(ctx, hash)
		}
		s.uncleared.Store(false)
	}
	k := s.Prefix + hex.EncodeToString(hash)
	env, err := s.rdb.Get(ctx, k).Result()
	switch {
	case err == nil && env != "":
		return env, nil
	case err != nil && !errors.Is(err, redis.Nil):
		s.Guard.Failed(ctx, "read", err)
		return s.Store.AuthenticateSDKKey(ctx, hash)
	}
	env, err = s.Store.AuthenticateSDKKey(ctx, hash)
	if err == nil {
		if err := s.rdb.Set(ctx, k, env, s.ttl).Err(); err != nil {
			s.Guard.Failed(ctx, "write", err)
		}
	}
	return env, err
}

func (s *AuthStore) RevokeSDKKey(ctx context.Context, actor string, id int64) error {
	if err := s.Store.RevokeSDKKey(ctx, actor, id); err != nil {
		return err
	}
	s.clearOrRemember(ctx)
	time.AfterFunc(s.RecheckAfter, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.clearOrRemember(ctx)
	})
	return nil
}

func (s *AuthStore) clearOrRemember(ctx context.Context) {
	if err := s.clearAll(ctx); err != nil {
		s.uncleared.Store(true)
		s.Guard.Warn(ctx, "clear", err)
	}
}

func (s *AuthStore) clearAll(ctx context.Context) error {
	iter := s.rdb.Scan(ctx, 0, s.Prefix+"*", 500).Iterator()
	for iter.Next(ctx) {
		if err := s.rdb.Del(ctx, iter.Val()).Err(); err != nil {
			return err
		}
	}
	return iter.Err()
}
