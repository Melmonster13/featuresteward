// Package store implements flag.Store on PostgreSQL.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Melmonster13/featuresteward/internal/eval"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/store/db"
)

type Postgres struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

var _ flag.Store = (*Postgres)(nil)

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool, q: db.New(pool)}
}

func (s *Postgres) CreateFlag(ctx context.Context, actor, key, name, description string) (flag.Flag, error) {
	if err := flag.ValidateMeta(key, name); err != nil {
		return flag.Flag{}, err
	}
	var out flag.Flag
	err := s.inTx(ctx, func(q *db.Queries) error {
		row, err := q.CreateFlag(ctx, db.CreateFlagParams{Key: key, Name: name, Description: description})
		if err != nil {
			return err
		}
		if err := q.CreateFlagEnvironments(ctx, row.ID); err != nil {
			return err
		}
		if err := audit(ctx, q, actor, flag.ActionCreated, key, "", nil, flag.Meta{Key: key, Name: name, Description: description}); err != nil {
			return err
		}
		out, err = load(ctx, q, row)
		return err
	})
	return out, err
}

func (s *Postgres) GetFlag(ctx context.Context, key string) (flag.Flag, error) {
	row, err := s.q.GetFlag(ctx, key)
	if err != nil {
		return flag.Flag{}, mapErr(err)
	}
	return load(ctx, s.q, row)
}

func (s *Postgres) ListFlags(ctx context.Context) ([]flag.Flag, error) {
	rows, err := s.q.ListFlags(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	envRows, err := s.q.ListFlagEnvironments(ctx, ids)
	if err != nil {
		return nil, err
	}
	envs := make(map[int64][]db.FlagEnvironment)
	for _, e := range envRows {
		envs[e.FlagID] = append(envs[e.FlagID], e)
	}
	out := make([]flag.Flag, len(rows))
	for i, r := range rows {
		if out[i], err = toFlag(r, envs[r.ID]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Postgres) UpdateFlag(ctx context.Context, actor, key, name, description string) (flag.Flag, error) {
	if err := flag.ValidateMeta(key, name); err != nil {
		return flag.Flag{}, err
	}
	var out flag.Flag
	err := s.inTx(ctx, func(q *db.Queries) error {
		before, err := lockActive(ctx, q, key)
		if err != nil {
			return err
		}
		row, err := q.UpdateFlag(ctx, db.UpdateFlagParams{ID: before.ID, Name: name, Description: description})
		if err != nil {
			return err
		}
		if err := audit(ctx, q, actor, flag.ActionUpdated, key, "",
			flag.Meta{Key: key, Name: before.Name, Description: before.Description},
			flag.Meta{Key: key, Name: name, Description: description}); err != nil {
			return err
		}
		out, err = load(ctx, q, row)
		return err
	})
	return out, err
}

func (s *Postgres) UpdateEnvironment(ctx context.Context, actor, key, env string, cfg flag.EnvConfig) (flag.Flag, error) {
	if err := cfg.Validate(); err != nil {
		return flag.Flag{}, err
	}
	if cfg.Rules == nil {
		cfg.Rules = []eval.Rule{}
	}
	rules, err := json.Marshal(cfg.Rules)
	if err != nil {
		return flag.Flag{}, err
	}
	var out flag.Flag
	err = s.inTx(ctx, func(q *db.Queries) error {
		f, err := lockActive(ctx, q, key)
		if err != nil {
			return err
		}
		prev, err := q.GetFlagEnvironmentForUpdate(ctx, db.GetFlagEnvironmentForUpdateParams{FlagID: f.ID, Environment: env})
		if err != nil {
			return mapErr(err)
		}
		before, err := toEnvConfig(prev.Enabled, prev.RolloutPercentage, prev.Rules)
		if err != nil {
			return err
		}
		if err := q.UpdateFlagEnvironment(ctx, db.UpdateFlagEnvironmentParams{
			FlagID:            f.ID,
			Environment:       env,
			Enabled:           cfg.Enabled,
			RolloutPercentage: int16(cfg.RolloutPercentage),
			Rules:             rules,
		}); err != nil {
			return err
		}
		if err := q.TouchFlag(ctx, f.ID); err != nil {
			return err
		}
		if err := audit(ctx, q, actor, flag.ActionEnvUpdated, key, env, before, cfg); err != nil {
			return err
		}
		row, err := q.GetFlag(ctx, key)
		if err != nil {
			return err
		}
		out, err = load(ctx, q, row)
		return err
	})
	return out, err
}

func (s *Postgres) ArchiveFlag(ctx context.Context, actor, key string) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		f, err := lockActive(ctx, q, key)
		if err != nil {
			return err
		}
		if err := q.ArchiveFlag(ctx, f.ID); err != nil {
			return err
		}
		return audit(ctx, q, actor, flag.ActionArchived, key, "", nil, nil)
	})
}

func (s *Postgres) EvalConfig(ctx context.Context, key, env string) (eval.Flag, error) {
	row, err := s.q.GetEvalConfig(ctx, db.GetEvalConfigParams{Key: key, Environment: env})
	if err != nil {
		return eval.Flag{}, mapErr(err)
	}
	cfg, err := toEnvConfig(row.Enabled, row.RolloutPercentage, row.Rules)
	if err != nil {
		return eval.Flag{}, err
	}
	return eval.Flag{Key: key, Enabled: cfg.Enabled, RolloutPercentage: cfg.RolloutPercentage, Rules: cfg.Rules}, nil
}

func (s *Postgres) ListEnvironments(ctx context.Context) ([]string, error) {
	return s.q.ListEnvironments(ctx)
}

func (s *Postgres) ListAuditEvents(ctx context.Context, flagKey string) ([]flag.AuditEvent, error) {
	rows, err := s.q.ListAuditEvents(ctx, &flagKey)
	if err != nil {
		return nil, err
	}
	out := make([]flag.AuditEvent, len(rows))
	for i, r := range rows {
		out[i] = flag.AuditEvent{
			ID:          r.ID,
			OccurredAt:  r.OccurredAt.Time,
			Actor:       r.Actor,
			Action:      r.Action,
			FlagKey:     deref(r.FlagKey),
			Environment: deref(r.Environment),
			Before:      r.Before,
			After:       r.After,
		}
	}
	return out, nil
}

func (s *Postgres) inTx(ctx context.Context, fn func(q *db.Queries) error) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return fn(s.q.WithTx(tx)) })
	return mapErr(err)
}

// lockActive locks a flag row for the rest of the transaction and
// rejects archived flags.
func lockActive(ctx context.Context, q *db.Queries, key string) (db.Flag, error) {
	f, err := q.GetFlagForUpdate(ctx, key)
	if err != nil {
		return db.Flag{}, mapErr(err)
	}
	if f.ArchivedAt.Valid {
		return db.Flag{}, fmt.Errorf("flag %q is archived: %w", key, flag.ErrNotFound)
	}
	return f, nil
}

func audit(ctx context.Context, q *db.Queries, actor, action, key, env string, before, after any) error {
	b, err := marshalOrNil(before)
	if err != nil {
		return err
	}
	a, err := marshalOrNil(after)
	if err != nil {
		return err
	}
	var envp *string
	if env != "" {
		envp = &env
	}
	return q.InsertAuditEvent(ctx, db.InsertAuditEventParams{
		Actor: actor, Action: action, FlagKey: &key, Environment: envp, Before: b, After: a,
	})
}

func marshalOrNil(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func load(ctx context.Context, q *db.Queries, row db.Flag) (flag.Flag, error) {
	envs, err := q.ListFlagEnvironments(ctx, []int64{row.ID})
	if err != nil {
		return flag.Flag{}, err
	}
	return toFlag(row, envs)
}

func toFlag(row db.Flag, envs []db.FlagEnvironment) (flag.Flag, error) {
	f := flag.Flag{
		Key:          row.Key,
		Name:         row.Name,
		Description:  row.Description,
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
		ArchivedAt:   timePtr(row.ArchivedAt),
		Environments: make(map[string]flag.EnvConfig, len(envs)),
	}
	for _, e := range envs {
		cfg, err := toEnvConfig(e.Enabled, e.RolloutPercentage, e.Rules)
		if err != nil {
			return flag.Flag{}, err
		}
		f.Environments[e.Environment] = cfg
	}
	return f, nil
}

func toEnvConfig(enabled bool, pct int16, rulesJSON []byte) (flag.EnvConfig, error) {
	var rules []eval.Rule
	if err := json.Unmarshal(rulesJSON, &rules); err != nil {
		return flag.EnvConfig{}, fmt.Errorf("decode rules: %w", err)
	}
	if rules == nil {
		rules = []eval.Rule{}
	}
	return flag.EnvConfig{Enabled: enabled, RolloutPercentage: int(pct), Rules: rules}, nil
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return flag.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return flag.ErrConflict
		case "23514": // check_violation; message is shown to API clients
			return errors.Join(flag.ErrInvalid, errors.New("value violates a database constraint"))
		}
	}
	return err
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
