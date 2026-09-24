// Package authtest provides an in-memory auth.Store and a contract test
// suite that every auth.Store implementation must pass.
package authtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/Melmonster13/featuresteward/internal/audit"
	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/errs"
)

type token struct {
	auth.Token
	hash   []byte
	userID int64
}

type session struct {
	hash      []byte
	tokenID   int64
	expiresAt time.Time
}

type sdkKey struct {
	auth.SDKKey
	hash []byte
}

type Memory struct {
	mu       sync.Mutex
	envs     []string
	users    map[string]*auth.User
	tokens   []*token
	sdkKeys  []*sdkKey
	sessions []*session
	events   []audit.Event
	nextID   int64
}

var _ auth.Store = (*Memory)(nil)

// NewMemory returns an empty store with environments dev, staging, and prod.
func NewMemory() *Memory {
	return &Memory{envs: []string{"dev", "staging", "prod"}, users: map[string]*auth.User{}}
}

func (m *Memory) CreateUser(_ context.Context, actor, handle, name string, role auth.Role) (auth.User, error) {
	if err := auth.ValidateUser(handle, role); err != nil {
		return auth.User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[handle]; ok {
		return auth.User{}, errs.ErrConflict
	}
	u := &auth.User{ID: m.id(), Handle: handle, Name: name, Role: role, CreatedAt: time.Now()}
	m.users[handle] = u
	m.audit(actor, auth.ActionUserCreated, handle, nil, meta(*u))
	return *u, nil
}

func (m *Memory) GetUser(_ context.Context, handle string) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[handle]
	if !ok {
		return auth.User{}, errs.ErrNotFound
	}
	return *u, nil
}

func (m *Memory) ListUsers(context.Context) ([]auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []auth.User{}
	for _, u := range m.users {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out, nil
}

func (m *Memory) SetRole(_ context.Context, actor, handle string, role auth.Role) (auth.User, error) {
	if !role.Valid() {
		return auth.User{}, auth.ValidateUser(handle, role)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u, err := m.enabled(handle)
	if err != nil {
		return auth.User{}, err
	}
	before := meta(*u)
	u.Role = role
	m.audit(actor, auth.ActionRoleChanged, handle, before, meta(*u))
	return *u, nil
}

func (m *Memory) DisableUser(_ context.Context, actor, handle string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, err := m.enabled(handle)
	if err != nil {
		return err
	}
	now := time.Now()
	u.DisabledAt = &now
	for _, t := range m.tokens {
		if t.userID == u.ID && t.RevokedAt == nil {
			t.RevokedAt = &now
		}
	}
	m.audit(actor, auth.ActionUserDisabled, handle, meta(*u), nil)
	return nil
}

func (m *Memory) CreateToken(_ context.Context, actor, handle, name string, hash []byte, prefix string, expiresAt *time.Time) (auth.Token, error) {
	if err := auth.ValidateToken(name, expiresAt, time.Now()); err != nil {
		return auth.Token{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u, err := m.enabled(handle)
	if err != nil {
		return auth.Token{}, err
	}
	for _, t := range m.tokens {
		if bytes.Equal(t.hash, hash) {
			return auth.Token{}, errs.ErrConflict
		}
	}
	t := &token{Token: auth.Token{ID: m.id(), Name: name, Prefix: prefix, CreatedAt: time.Now(), ExpiresAt: expiresAt},
		hash: bytes.Clone(hash), userID: u.ID}
	m.tokens = append(m.tokens, t)
	m.audit(actor, auth.ActionTokenCreated, handle, nil, tokenMeta(t.Token))
	return t.Token, nil
}

func (m *Memory) ListTokens(_ context.Context, handle string) ([]auth.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[handle]
	if !ok {
		return nil, errs.ErrNotFound
	}
	out := []auth.Token{}
	for _, t := range m.tokens {
		if t.userID == u.ID {
			out = append(out, t.Token)
		}
	}
	return out, nil
}

func (m *Memory) RevokeToken(_ context.Context, actor, handle string, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[handle]
	if !ok {
		return errs.ErrNotFound
	}
	for _, t := range m.tokens {
		if t.ID == id && t.userID == u.ID && t.RevokedAt == nil {
			now := time.Now()
			t.RevokedAt = &now
			m.audit(actor, auth.ActionTokenRevoked, handle, tokenMeta(t.Token), nil)
			return nil
		}
	}
	return errs.ErrNotFound
}

func (m *Memory) Authenticate(_ context.Context, hash []byte) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, u := m.active(func(t *token) bool { return bytes.Equal(t.hash, hash) })
	if u == nil {
		return auth.User{}, errs.ErrUnauthorized
	}
	now := time.Now()
	t.LastUsedAt = &now
	return *u, nil
}

// active finds an unrevoked, unexpired token matching match, and its
// enabled user. u is nil if there is none.
func (m *Memory) active(match func(*token) bool) (t *token, u *auth.User) {
	now := time.Now()
	for _, t := range m.tokens {
		if !match(t) || t.RevokedAt != nil || (t.ExpiresAt != nil && !t.ExpiresAt.After(now)) {
			continue
		}
		for _, u := range m.users {
			if u.ID == t.userID && u.DisabledAt == nil {
				return t, u
			}
		}
	}
	return nil, nil
}

func (m *Memory) CreateSession(_ context.Context, tokenHash, sessionHash []byte, expiresAt time.Time) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, u := m.active(func(t *token) bool { return bytes.Equal(t.hash, tokenHash) })
	if u == nil {
		return auth.User{}, errs.ErrUnauthorized
	}
	for _, s := range m.sessions {
		if bytes.Equal(s.hash, sessionHash) {
			return auth.User{}, errs.ErrConflict
		}
	}
	now := time.Now()
	t.LastUsedAt = &now
	m.sessions = append(m.sessions, &session{hash: bytes.Clone(sessionHash), tokenID: t.ID, expiresAt: expiresAt})
	return *u, nil
}

func (m *Memory) AuthenticateSession(_ context.Context, hash []byte) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if bytes.Equal(s.hash, hash) && s.expiresAt.After(time.Now()) {
			if _, u := m.active(func(t *token) bool { return t.ID == s.tokenID }); u != nil {
				return *u, nil
			}
		}
	}
	return auth.User{}, errs.ErrUnauthorized
}

func (m *Memory) DeleteSession(_ context.Context, hash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions = slices.DeleteFunc(m.sessions, func(s *session) bool { return bytes.Equal(s.hash, hash) })
	return nil
}

func (m *Memory) DeleteExpiredSessions(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.sessions = slices.DeleteFunc(m.sessions, func(s *session) bool { return !s.expiresAt.After(now) })
	return nil
}

func (m *Memory) ListUserAuditEvents(_ context.Context, handle string) ([]audit.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []audit.Event{}
	for _, e := range m.events {
		if e.SubjectUser == handle {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *Memory) CreateSDKKey(_ context.Context, actor, env, name string, hash []byte, prefix string) (auth.SDKKey, error) {
	if err := auth.ValidateToken(name, nil, time.Now()); err != nil {
		return auth.SDKKey{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !slices.Contains(m.envs, env) {
		return auth.SDKKey{}, errs.ErrNotFound
	}
	for _, k := range m.sdkKeys {
		if bytes.Equal(k.hash, hash) {
			return auth.SDKKey{}, errs.ErrConflict
		}
	}
	k := &sdkKey{SDKKey: auth.SDKKey{ID: m.id(), Environment: env, Name: name, Prefix: prefix, CreatedAt: time.Now()},
		hash: bytes.Clone(hash)}
	m.sdkKeys = append(m.sdkKeys, k)
	m.envAudit(actor, auth.ActionSDKKeyCreated, env, nil, sdkKeyMeta(k.SDKKey))
	return k.SDKKey, nil
}

func (m *Memory) ListSDKKeys(context.Context) ([]auth.SDKKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []auth.SDKKey{}
	for _, k := range m.sdkKeys {
		out = append(out, k.SDKKey)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Environment < out[j].Environment })
	return out, nil
}

func (m *Memory) RevokeSDKKey(_ context.Context, actor string, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.sdkKeys {
		if k.ID == id && k.RevokedAt == nil {
			now := time.Now()
			k.RevokedAt = &now
			m.envAudit(actor, auth.ActionSDKKeyRevoked, k.Environment, sdkKeyMeta(k.SDKKey), nil)
			return nil
		}
	}
	return errs.ErrNotFound
}

func (m *Memory) AuthenticateSDKKey(_ context.Context, hash []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.sdkKeys {
		if bytes.Equal(k.hash, hash) && k.RevokedAt == nil {
			return k.Environment, nil
		}
	}
	return "", errs.ErrUnauthorized
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

func (m *Memory) id() int64 {
	m.nextID++
	return m.nextID
}

func (m *Memory) enabled(handle string) (*auth.User, error) {
	u, ok := m.users[handle]
	if !ok {
		return nil, errs.ErrNotFound
	}
	if u.DisabledAt != nil {
		return nil, fmt.Errorf("user %q is disabled: %w", handle, errs.ErrNotFound)
	}
	return u, nil
}

func (m *Memory) audit(actor, action, handle string, before, after any) {
	m.events = append(m.events, audit.Event{
		ID: int64(len(m.events) + 1), OccurredAt: time.Now(), Actor: actor, Action: action,
		SubjectUser: handle, Before: marshalOrNil(before), After: marshalOrNil(after),
	})
}

func (m *Memory) envAudit(actor, action, env string, before, after any) {
	m.events = append(m.events, audit.Event{
		ID: int64(len(m.events) + 1), OccurredAt: time.Now(), Actor: actor, Action: action,
		Environment: env, Before: marshalOrNil(before), After: marshalOrNil(after),
	})
}

func sdkKeyMeta(k auth.SDKKey) auth.SDKKeyMeta {
	return auth.SDKKeyMeta{ID: k.ID, Environment: k.Environment, Name: k.Name, Prefix: k.Prefix}
}

func meta(u auth.User) auth.UserMeta {
	return auth.UserMeta{Handle: u.Handle, Name: u.Name, Role: u.Role}
}

func tokenMeta(t auth.Token) auth.TokenMeta {
	return auth.TokenMeta{ID: t.ID, Name: t.Name, Prefix: t.Prefix, ExpiresAt: t.ExpiresAt}
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
