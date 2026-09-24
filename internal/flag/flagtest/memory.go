// Package flagtest provides an in-memory flag.Store and a contract test
// suite that every flag.Store implementation must pass.
package flagtest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Melmonster13/featuresteward/internal/audit"
	"github.com/Melmonster13/featuresteward/internal/eval"
	"github.com/Melmonster13/featuresteward/internal/flag"
)

type Memory struct {
	mu     sync.Mutex
	envs   []flag.Environment
	flags  map[string]*flag.Flag
	events []audit.Event
}

var _ flag.Store = (*Memory)(nil)

// NewMemory returns an empty store with the default environments.
func NewMemory() *Memory {
	return &Memory{envs: []flag.Environment{
		{Key: "dev", Name: "Development"},
		{Key: "prod", Name: "Production", Protected: true},
		{Key: "staging", Name: "Staging"},
	}, flags: map[string]*flag.Flag{}}
}

func (m *Memory) CreateFlag(_ context.Context, actor, key, name, description string) (flag.Flag, error) {
	if err := flag.ValidateMeta(key, name); err != nil {
		return flag.Flag{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.flags[key]; ok {
		return flag.Flag{}, flag.ErrConflict
	}
	now := time.Now()
	f := &flag.Flag{Key: key, Name: name, Description: description, CreatedAt: now, UpdatedAt: now,
		Environments: map[string]flag.EnvConfig{}}
	for _, e := range m.envs {
		f.Environments[e.Key] = flag.EnvConfig{RolloutPercentage: 100, Rules: []eval.Rule{}}
	}
	m.flags[key] = f
	m.audit(actor, flag.ActionCreated, key, "", nil, flag.Meta{Key: key, Name: name, Description: description})
	return clone(f), nil
}

func (m *Memory) GetFlag(_ context.Context, key string) (flag.Flag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.flags[key]
	if !ok {
		return flag.Flag{}, flag.ErrNotFound
	}
	return clone(f), nil
}

func (m *Memory) ListFlags(context.Context) ([]flag.Flag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []flag.Flag{}
	for _, f := range m.flags {
		if f.ArchivedAt == nil {
			out = append(out, clone(f))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *Memory) UpdateFlag(_ context.Context, actor, key, name, description string) (flag.Flag, error) {
	if err := flag.ValidateMeta(key, name); err != nil {
		return flag.Flag{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.active(key)
	if err != nil {
		return flag.Flag{}, err
	}
	before := flag.Meta{Key: key, Name: f.Name, Description: f.Description}
	f.Name, f.Description, f.UpdatedAt = name, description, time.Now()
	m.audit(actor, flag.ActionUpdated, key, "", before, flag.Meta{Key: key, Name: name, Description: description})
	return clone(f), nil
}

func (m *Memory) UpdateEnvironment(_ context.Context, actor, key, env string, cfg flag.EnvConfig) (flag.Flag, error) {
	if err := cfg.Validate(); err != nil {
		return flag.Flag{}, err
	}
	cfg.Rules = cloneRules(cfg.Rules)
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.active(key)
	if err != nil {
		return flag.Flag{}, err
	}
	before, ok := f.Environments[env]
	if !ok {
		return flag.Flag{}, flag.ErrNotFound
	}
	f.Environments[env] = cfg
	f.UpdatedAt = time.Now()
	m.audit(actor, flag.ActionEnvUpdated, key, env, before, cfg)
	return clone(f), nil
}

func (m *Memory) ArchiveFlag(_ context.Context, actor, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.active(key)
	if err != nil {
		return err
	}
	now := time.Now()
	f.ArchivedAt, f.UpdatedAt = &now, now
	m.audit(actor, flag.ActionArchived, key, "", nil, nil)
	return nil
}

func (m *Memory) EvalConfig(_ context.Context, key, env string) (eval.Flag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.active(key)
	if err != nil {
		return eval.Flag{}, err
	}
	cfg, ok := f.Environments[env]
	if !ok {
		return eval.Flag{}, flag.ErrNotFound
	}
	return eval.Flag{Key: key, Enabled: cfg.Enabled, RolloutPercentage: cfg.RolloutPercentage, Rules: cloneRules(cfg.Rules)}, nil
}

func (m *Memory) ListEnvironments(context.Context) ([]flag.Environment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.envs), nil
}

func (m *Memory) GetEnvironment(_ context.Context, key string) (flag.Environment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.envs {
		if e.Key == key {
			return e, nil
		}
	}
	return flag.Environment{}, flag.ErrNotFound
}

func (m *Memory) CreateEnvironment(_ context.Context, actor string, env flag.Environment) (flag.Environment, error) {
	if err := env.Validate(); err != nil {
		return flag.Environment{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.envs {
		if e.Key == env.Key {
			return flag.Environment{}, flag.ErrConflict
		}
	}
	m.envs = append(m.envs, env)
	slices.SortFunc(m.envs, func(a, b flag.Environment) int { return strings.Compare(a.Key, b.Key) })
	for _, f := range m.flags {
		f.Environments[env.Key] = flag.EnvConfig{RolloutPercentage: 100, Rules: []eval.Rule{}}
	}
	m.auditEnv(actor, flag.ActionEnvironmentCreated, env.Key, nil, env)
	return env, nil
}

func (m *Memory) UpdateEnvironmentSettings(_ context.Context, actor string, env flag.Environment) (flag.Environment, error) {
	if err := env.Validate(); err != nil {
		return flag.Environment{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.envs {
		if e.Key == env.Key {
			m.envs[i] = env
			m.auditEnv(actor, flag.ActionEnvironmentUpdated, env.Key, e, env)
			return env, nil
		}
	}
	return flag.Environment{}, flag.ErrNotFound
}

func (m *Memory) ListEnvironmentAuditEvents(_ context.Context, env string) ([]audit.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []audit.Event{}
	for _, e := range m.events {
		if e.Environment == env && e.FlagKey == "" {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *Memory) ListAuditEvents(_ context.Context, flagKey string) ([]audit.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []audit.Event{}
	for _, e := range m.events {
		if e.FlagKey == flagKey {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *Memory) active(key string) (*flag.Flag, error) {
	f, ok := m.flags[key]
	if !ok {
		return nil, flag.ErrNotFound
	}
	if f.ArchivedAt != nil {
		return nil, fmt.Errorf("flag %q is archived: %w", key, flag.ErrNotFound)
	}
	return f, nil
}

func (m *Memory) audit(actor, action, key, env string, before, after any) {
	m.events = append(m.events, audit.Event{
		ID: int64(len(m.events) + 1), OccurredAt: time.Now(), Actor: actor, Action: action,
		FlagKey: key, Environment: env, Before: marshalOrNil(before), After: marshalOrNil(after),
	})
}

func (m *Memory) auditEnv(actor, action, env string, before, after any) {
	m.events = append(m.events, audit.Event{
		ID: int64(len(m.events) + 1), OccurredAt: time.Now(), Actor: actor, Action: action,
		Environment: env, Before: marshalOrNil(before), After: marshalOrNil(after),
	})
}

func marshalOrNil(v any) []byte {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func clone(f *flag.Flag) flag.Flag {
	c := *f
	c.Environments = make(map[string]flag.EnvConfig, len(f.Environments))
	for k, v := range f.Environments {
		v.Rules = cloneRules(v.Rules)
		c.Environments[k] = v
	}
	return c
}

func cloneRules(rules []eval.Rule) []eval.Rule {
	out := make([]eval.Rule, len(rules))
	for i, r := range rules {
		r.Values = slices.Clone(r.Values)
		out[i] = r
	}
	return out
}
