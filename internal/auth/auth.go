// Package auth holds users, roles, API tokens, and their storage contract.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/Melmonster13/featuresteward/internal/audit"
	"github.com/Melmonster13/featuresteward/internal/errs"
)

type Role string

const (
	RoleViewer   Role = "viewer"
	RoleEditor   Role = "editor"
	RoleApprover Role = "approver"
	RoleAdmin    Role = "admin"
)

var roleRank = map[Role]int{RoleViewer: 1, RoleEditor: 2, RoleApprover: 3, RoleAdmin: 4}

func (r Role) Valid() bool { return roleRank[r] > 0 }

// AtLeast reports whether r includes min's permissions. Roles are
// cumulative: admin > approver > editor > viewer.
func (r Role) AtLeast(min Role) bool { return r.Valid() && roleRank[r] >= roleRank[min] }

var handlePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type User struct {
	ID         int64
	Handle     string
	Name       string
	Role       Role
	CreatedAt  time.Time
	DisabledAt *time.Time
}

// Token describes an API token. The secret is never stored or returned
// after creation.
type Token struct {
	ID         int64
	Name       string
	Prefix     string // first characters of the secret, for recognizing it
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Audit snapshots. These must never include token hashes or secrets.
type UserMeta struct {
	Handle string `json:"handle"`
	Name   string `json:"name"`
	Role   Role   `json:"role"`
}

type TokenMeta struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// SDKKey lets an application evaluate flags in one environment.
type SDKKey struct {
	ID          int64
	Environment string
	Name        string
	Prefix      string
	CreatedAt   time.Time
	RevokedAt   *time.Time
}

type SDKKeyMeta struct {
	ID          int64  `json:"id"`
	Environment string `json:"environment"`
	Name        string `json:"name"`
	Prefix      string `json:"prefix"`
}

const (
	ActionUserCreated   = "user.created"
	ActionRoleChanged   = "user.role_changed"
	ActionUserDisabled  = "user.disabled"
	ActionTokenCreated  = "token.created"
	ActionTokenRevoked  = "token.revoked"
	ActionSDKKeyCreated = "sdk_key.created"
	ActionSDKKeyRevoked = "sdk_key.revoked"
)

// Secret prefixes. They differ so the API can tell which kind of
// credential it was given, and so leaked keys are easy to recognize.
const (
	TokenPrefix  = "fs_"
	SDKKeyPrefix = "fs_sdk_"
)

// NewSecret returns a random secret with the given prefix, its SHA-256
// hash for storage, and a short display prefix.
func NewSecret(prefix string) (secret string, hash []byte, display string) {
	b := make([]byte, 32)
	rand.Read(b) // never returns an error
	secret = prefix + hex.EncodeToString(b)
	return secret, HashSecret(secret), secret[:len(prefix)+8]
}

func HashSecret(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

func ValidateUser(handle string, role Role) error {
	if !handlePattern.MatchString(handle) {
		return errs.Invalid("handle must be 1-64 lowercase letters, digits, '.', '_' or '-'")
	}
	if !role.Valid() {
		return errs.Invalid("role must be viewer, editor, approver, or admin")
	}
	return nil
}

func ValidateToken(name string, expiresAt *time.Time, now time.Time) error {
	if strings.TrimSpace(name) == "" {
		return errs.Invalid("token name is required")
	}
	if expiresAt != nil && !expiresAt.After(now) {
		return errs.Invalid("expires_at must be in the future")
	}
	return nil
}

// Store persists users and tokens. Mutations write their audit event in
// the same transaction. Disabled users can't authenticate or be changed.
type Store interface {
	CreateUser(ctx context.Context, actor, handle, name string, role Role) (User, error)
	GetUser(ctx context.Context, handle string) (User, error)
	ListUsers(ctx context.Context) ([]User, error)
	SetRole(ctx context.Context, actor, handle string, role Role) (User, error)
	// DisableUser also revokes all of the user's tokens.
	DisableUser(ctx context.Context, actor, handle string) error

	// CreateToken stores a token for handle. Generate hash and prefix with NewSecret.
	CreateToken(ctx context.Context, actor, handle, name string, hash []byte, prefix string, expiresAt *time.Time) (Token, error)
	ListTokens(ctx context.Context, handle string) ([]Token, error)
	RevokeToken(ctx context.Context, actor, handle string, id int64) error

	// Authenticate returns the user owning an active token with this hash,
	// or errs.ErrUnauthorized.
	Authenticate(ctx context.Context, hash []byte) (User, error)

	// ListUserAuditEvents returns events about a user and their tokens, oldest first.
	ListUserAuditEvents(ctx context.Context, handle string) ([]audit.Event, error)

	// CreateSDKKey stores a key for env. Generate hash and prefix with
	// NewSecret(SDKKeyPrefix). An unknown env is errs.ErrNotFound.
	CreateSDKKey(ctx context.Context, actor, env, name string, hash []byte, prefix string) (SDKKey, error)
	ListSDKKeys(ctx context.Context) ([]SDKKey, error)
	RevokeSDKKey(ctx context.Context, actor string, id int64) error
	// AuthenticateSDKKey returns the environment of an active key with
	// this hash, or errs.ErrUnauthorized.
	AuthenticateSDKKey(ctx context.Context, hash []byte) (env string, err error)
	// ListEnvironmentAuditEvents returns environment-level events (not
	// flag changes), such as SDK key changes, oldest first.
	ListEnvironmentAuditEvents(ctx context.Context, env string) ([]audit.Event, error)
}
