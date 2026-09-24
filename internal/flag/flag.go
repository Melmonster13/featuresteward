// Package flag holds the flag domain types and the storage contract.
package flag

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/Melmonster13/featuresteward/internal/eval"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
	ErrInvalid  = errors.New("invalid")
)

var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func ValidKey(key string) bool { return len(key) <= 100 && keyPattern.MatchString(key) }

type Flag struct {
	Key          string
	Name         string
	Description  string
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

// Meta is the audit snapshot of a flag's descriptive fields.
type Meta struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func ValidateMeta(key, name string) error {
	if !ValidKey(key) {
		return errors.Join(ErrInvalid, errors.New("key must be lowercase letters, digits, and dashes"))
	}
	if name == "" {
		return errors.Join(ErrInvalid, errors.New("name is required"))
	}
	return nil
}

func (c EnvConfig) Validate() error {
	if c.RolloutPercentage < 0 || c.RolloutPercentage > 100 {
		return errors.Join(ErrInvalid, errors.New("rollout_percentage must be 0-100"))
	}
	for _, r := range c.Rules {
		if r.Attribute != eval.AttributeUserID && r.Attribute != eval.AttributeGroup {
			return errors.Join(ErrInvalid, errors.New("rule attribute must be user_id or group"))
		}
		if len(r.Values) == 0 {
			return errors.Join(ErrInvalid, errors.New("rule values must not be empty"))
		}
	}
	return nil
}

type AuditEvent struct {
	ID          int64
	OccurredAt  time.Time
	Actor       string
	Action      string
	FlagKey     string
	Environment string
	Before      []byte // JSON, nil when absent
	After       []byte
}

const (
	ActionCreated    = "flag.created"
	ActionUpdated    = "flag.updated"
	ActionEnvUpdated = "flag.environment_updated"
	ActionArchived   = "flag.archived"
)

// Store persists flags. Every mutation writes its audit event in the
// same transaction, so a change is never recorded without its history.
//
// Archived flags are still returned by GetFlag but excluded from
// ListFlags and EvalConfig, and cannot be modified.
type Store interface {
	// CreateFlag creates the flag, disabled, in every environment.
	CreateFlag(ctx context.Context, actor, key, name, description string) (Flag, error)
	GetFlag(ctx context.Context, key string) (Flag, error)
	ListFlags(ctx context.Context) ([]Flag, error)
	UpdateFlag(ctx context.Context, actor, key, name, description string) (Flag, error)
	UpdateEnvironment(ctx context.Context, actor, key, env string, cfg EnvConfig) (Flag, error)
	ArchiveFlag(ctx context.Context, actor, key string) error

	// EvalConfig returns what eval.Evaluate needs for one flag in one environment.
	EvalConfig(ctx context.Context, key, env string) (eval.Flag, error)

	ListEnvironments(ctx context.Context) ([]string, error)
	// ListAuditEvents returns a flag's events, oldest first.
	ListAuditEvents(ctx context.Context, flagKey string) ([]AuditEvent, error)
}
