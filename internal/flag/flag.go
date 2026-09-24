// Package flag holds the flag domain types and the storage contract.
package flag

import (
	"context"
	"regexp"
	"time"

	"github.com/Melmonster13/featuresteward/internal/audit"
	"github.com/Melmonster13/featuresteward/internal/errs"
	"github.com/Melmonster13/featuresteward/internal/eval"
)

// Aliases so callers can match flag errors without importing errs.
var (
	ErrNotFound = errs.ErrNotFound
	ErrConflict = errs.ErrConflict
	ErrInvalid  = errs.ErrInvalid
)

var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func ValidKey(key string) bool { return len(key) <= 100 && keyPattern.MatchString(key) }

type Flag struct {
	Key         string
	Name        string
	Description string
	// Steward is the accountable user's handle; "" means unassigned.
	Steward      string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ArchivedAt   *time.Time
	Environments map[string]EnvConfig
}

// EnvConfig is a flag's state in one environment. Rules is never nil
// when returned by a Store.
type EnvConfig struct {
	Enabled           bool        `json:"enabled"`
	RolloutPercentage int         `json:"rollout_percentage"`
	Rules             []eval.Rule `json:"rules"`
}

type Environment struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Protected environments (prod by default) need an admin to change
	// their flags.
	Protected bool `json:"protected"`
}

// Meta is the audit snapshot of a flag's descriptive fields.
type Meta struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Steward     string `json:"steward,omitempty"`
}

// StewardSnapshot is the audit snapshot for a steward change.
type StewardSnapshot struct {
	Steward *string `json:"steward"` // null when unassigned
}

func NewStewardSnapshot(handle string) StewardSnapshot {
	if handle == "" {
		return StewardSnapshot{}
	}
	return StewardSnapshot{Steward: &handle}
}

func (e Environment) Validate() error {
	if len(e.Key) > 32 || !envKeyPattern.MatchString(e.Key) {
		return errs.Invalid("environment key must be 1-32 lowercase letters, digits, and dashes")
	}
	if e.Name == "" {
		return errs.Invalid("name is required")
	}
	return nil
}

var envKeyPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

func ValidateMeta(key, name string) error {
	if !ValidKey(key) {
		return errs.Invalid("key must be lowercase letters, digits, and dashes")
	}
	if name == "" {
		return errs.Invalid("name is required")
	}
	return nil
}

func (c EnvConfig) Validate() error {
	if c.RolloutPercentage < 0 || c.RolloutPercentage > 100 {
		return errs.Invalid("rollout_percentage must be 0-100")
	}
	for _, r := range c.Rules {
		if r.Attribute != eval.AttributeUserID && r.Attribute != eval.AttributeGroup {
			return errs.Invalid("rule attribute must be user_id or group")
		}
		if len(r.Values) == 0 {
			return errs.Invalid("rule values must not be empty")
		}
	}
	return nil
}

const (
	ActionCreated    = "flag.created"
	ActionUpdated    = "flag.updated"
	ActionEnvUpdated = "flag.environment_updated"
	ActionArchived   = "flag.archived"
	ActionSteward    = "flag.steward_changed"

	ActionEnvironmentCreated = "environment.created"
	ActionEnvironmentUpdated = "environment.updated"
)

// Store persists flags. Every mutation writes its audit event in the
// same transaction, so a change is never recorded without its history.
//
// Archived flags are still returned by GetFlag but excluded from
// ListFlags and EvalConfig, and cannot be modified.
type Store interface {
	// CreateFlag creates the flag, disabled, in every environment.
	// steward may be "" for none; callers validate the handle.
	CreateFlag(ctx context.Context, actor, key, name, description, steward string) (Flag, error)
	GetFlag(ctx context.Context, key string) (Flag, error)
	ListFlags(ctx context.Context) ([]Flag, error)
	UpdateFlag(ctx context.Context, actor, key, name, description string) (Flag, error)
	// UpdateEnvironment sets a flag's config in env. A non-empty reason
	// marks an emergency change, recorded in the audit event.
	UpdateEnvironment(ctx context.Context, actor, key, env string, cfg EnvConfig, reason string) (Flag, error)
	// SetSteward assigns a non-empty steward; callers validate the handle.
	SetSteward(ctx context.Context, actor, key, steward string) (Flag, error)
	ArchiveFlag(ctx context.Context, actor, key string) error

	// EvalConfig returns what eval.Evaluate needs for one flag in one environment.
	EvalConfig(ctx context.Context, key, env string) (eval.Flag, error)

	// ListEnvironments returns environments sorted by key.
	ListEnvironments(ctx context.Context) ([]Environment, error)
	GetEnvironment(ctx context.Context, key string) (Environment, error)
	// CreateEnvironment also gives every existing flag default settings
	// (disabled, 100%) in the new environment.
	CreateEnvironment(ctx context.Context, actor string, env Environment) (Environment, error)
	UpdateEnvironmentSettings(ctx context.Context, actor string, env Environment) (Environment, error)
	// ListEnvironmentAuditEvents returns environment-level events (not
	// flag changes), oldest first.
	ListEnvironmentAuditEvents(ctx context.Context, env string) ([]audit.Event, error)
	// ListAuditEvents returns a flag's events, oldest first.
	ListAuditEvents(ctx context.Context, flagKey string) ([]audit.Event, error)

	// CreateChangeRequest proposes cfg for a flag in env, based on its
	// current config there. Only one request can be pending per flag and
	// environment (ErrConflict), and cfg must differ from the current
	// config (ErrInvalid).
	CreateChangeRequest(ctx context.Context, actor, key, env string, cfg EnvConfig, reason string, expiresAt time.Time) (ChangeRequest, error)
	GetChangeRequest(ctx context.Context, id int64) (ChangeRequest, error)
	// ListChangeRequests returns matching requests, newest first.
	ListChangeRequests(ctx context.Context, filter RequestFilter) ([]ChangeRequest, error)
	// ApproveChangeRequest applies a pending request's config and marks it
	// approved, in one transaction. The requester can't approve it
	// (ErrForbidden). It fails with ErrConflict if the request isn't
	// pending, has expired, or the environment changed since it was made.
	ApproveChangeRequest(ctx context.Context, actor string, id int64, comment string) (ChangeRequest, error)
	// RejectChangeRequest closes a pending request without applying it.
	// The requester cancels instead (ErrForbidden).
	RejectChangeRequest(ctx context.Context, actor string, id int64, comment string) (ChangeRequest, error)
	// CancelChangeRequest withdraws a pending request. Only its requester
	// can (ErrForbidden).
	CancelChangeRequest(ctx context.Context, actor string, id int64) (ChangeRequest, error)
	// ExpireChangeRequests marks pending requests past their expiry as
	// expired and returns how many it marked.
	ExpireChangeRequests(ctx context.Context) (int, error)
}
