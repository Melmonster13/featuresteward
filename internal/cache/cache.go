// Package cache keeps flag evaluation settings in Redis in front of a
// flag.Store.
//
// Only EvalConfig is cached. Every change that affects evaluation clears
// the entries it touches after the change is saved, and again a moment
// later, so a read racing the change can't keep a stale value for long.
//
// If Redis fails, reads go to the store, and Redis is skipped for a few
// seconds so an outage doesn't add a timeout to every evaluation. Changes
// made while Redis is unreachable can't clear it, so an old entry can be
// served until it expires: at most the TTL.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Melmonster13/featuresteward/internal/errs"
	"github.com/Melmonster13/featuresteward/internal/eval"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/redisguard"
)

// missing marks a flag and environment that have no evaluation config,
// so lookups of unknown flags don't reach the database either.
const missing = "missing"

// Store is a flag.Store whose EvalConfig reads through Redis.
type Store struct {
	flag.Store
	rdb redis.UniversalClient
	ttl time.Duration

	// Prefix namespaces keys; change the version when the format changes.
	Prefix string
	// RecheckAfter is when the second clear runs after a change.
	RecheckAfter time.Duration
	// Guard skips Redis for a while after it fails.
	Guard *redisguard.Guard
}

var _ flag.Store = (*Store)(nil)

func New(inner flag.Store, rdb redis.UniversalClient, ttl time.Duration, log *slog.Logger) *Store {
	return &Store{Store: inner, rdb: rdb, ttl: ttl, Prefix: "fs:eval:v1:", RecheckAfter: time.Second,
		Guard: redisguard.New("evaluations read the database", log)}
}

func (s *Store) key(flagKey, env string) string { return s.Prefix + env + ":" + flagKey }

func (s *Store) EvalConfig(ctx context.Context, key, env string) (eval.Flag, error) {
	if !s.Guard.Up() {
		return s.Store.EvalConfig(ctx, key, env)
	}
	k := s.key(key, env)
	val, err := s.rdb.Get(ctx, k).Result()
	switch {
	case err == nil && val == missing:
		return eval.Flag{}, flag.ErrNotFound
	case err == nil:
		var f eval.Flag
		if json.Unmarshal([]byte(val), &f) == nil {
			return f, nil
		}
		// A corrupt entry is a miss; the set below replaces it.
	case !errors.Is(err, redis.Nil):
		s.Guard.Failed(ctx, "read", err)
		return s.Store.EvalConfig(ctx, key, env)
	}

	f, err := s.Store.EvalConfig(ctx, key, env)
	switch {
	case errors.Is(err, errs.ErrNotFound):
		s.set(ctx, k, missing)
	case err != nil:
		return f, err
	default:
		if b, merr := json.Marshal(f); merr == nil {
			s.set(ctx, k, string(b))
		}
	}
	return f, err
}

func (s *Store) set(ctx context.Context, k, v string) {
	if err := s.rdb.Set(ctx, k, v, s.ttl).Err(); err != nil {
		s.Guard.Failed(ctx, "write", err)
	}
}

// clear removes cache entries now and again after RecheckAfter, in case a
// read that began before the change stores the old value in between.
func (s *Store) clear(ctx context.Context, keys ...string) {
	if len(keys) == 0 {
		return
	}
	if err := s.rdb.Del(ctx, keys...).Err(); err != nil {
		s.Guard.Warn(ctx, "clear", err)
	}
	time.AfterFunc(s.RecheckAfter, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.rdb.Del(ctx, keys...).Err(); err != nil {
			s.Guard.Warn(ctx, "clear", err)
		}
	})
}

// clearFlag removes a flag's entries in every environment.
func (s *Store) clearFlag(ctx context.Context, key string) {
	envs, err := s.Store.ListEnvironments(ctx)
	if err != nil {
		s.Guard.Warn(ctx, "list environments to clear", err)
		return
	}
	keys := make([]string, len(envs))
	for i, e := range envs {
		keys[i] = s.key(key, e.Key)
	}
	s.clear(ctx, keys...)
}

// --- Changes that affect evaluation ---

func (s *Store) CreateFlag(ctx context.Context, actor, key, name, description, steward string) (flag.Flag, error) {
	f, err := s.Store.CreateFlag(ctx, actor, key, name, description, steward)
	if err == nil {
		s.clearFlag(ctx, key) // it may be cached as missing
	}
	return f, err
}

func (s *Store) UpdateEnvironment(ctx context.Context, actor, key, env string, cfg flag.EnvConfig, reason string) (flag.Flag, error) {
	f, err := s.Store.UpdateEnvironment(ctx, actor, key, env, cfg, reason)
	if err == nil {
		s.clear(ctx, s.key(key, env))
	}
	return f, err
}

func (s *Store) ArchiveFlag(ctx context.Context, actor, key string) error {
	err := s.Store.ArchiveFlag(ctx, actor, key)
	if err == nil {
		s.clearFlag(ctx, key)
	}
	return err
}

func (s *Store) ApproveChangeRequest(ctx context.Context, actor string, id int64, comment string) (flag.ChangeRequest, error) {
	r, err := s.Store.ApproveChangeRequest(ctx, actor, id, comment)
	if err == nil {
		s.clear(ctx, s.key(r.FlagKey, r.Environment))
	}
	return r, err
}

// CreateEnvironment gives every flag settings in the new environment, so
// any of them may be cached as missing there.
func (s *Store) CreateEnvironment(ctx context.Context, actor string, env flag.Environment) (flag.Environment, error) {
	e, err := s.Store.CreateEnvironment(ctx, actor, env)
	if err != nil {
		return e, err
	}
	var keys []string
	iter := s.rdb.Scan(ctx, 0, s.Prefix+env.Key+":*", 500).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		s.Guard.Warn(ctx, "scan", err)
	}
	s.clear(ctx, keys...)
	return e, nil
}
