package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Melmonster13/featuresteward/internal/audit"
	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/errs"
	"github.com/Melmonster13/featuresteward/internal/store/db"
)

var _ auth.Store = (*Postgres)(nil)

func (s *Postgres) CreateUser(ctx context.Context, actor, handle, name string, role auth.Role) (auth.User, error) {
	if err := auth.ValidateUser(handle, role); err != nil {
		return auth.User{}, err
	}
	var out auth.User
	err := s.inTx(ctx, func(q *db.Queries) error {
		row, err := q.CreateUser(ctx, db.CreateUserParams{Handle: handle, Name: name, Role: string(role)})
		if err != nil {
			return err
		}
		out = toUser(row)
		return userAudit(ctx, q, actor, auth.ActionUserCreated, handle, nil, userMeta(out))
	})
	return out, err
}

func (s *Postgres) GetUser(ctx context.Context, handle string) (auth.User, error) {
	row, err := s.q.GetUser(ctx, handle)
	if err != nil {
		return auth.User{}, mapErr(err)
	}
	return toUser(row), nil
}

func (s *Postgres) ListUsers(ctx context.Context) ([]auth.User, error) {
	rows, err := s.q.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]auth.User, len(rows))
	for i, r := range rows {
		out[i] = toUser(r)
	}
	return out, nil
}

func (s *Postgres) SetRole(ctx context.Context, actor, handle string, role auth.Role) (auth.User, error) {
	if !role.Valid() {
		return auth.User{}, auth.ValidateUser(handle, role)
	}
	var out auth.User
	err := s.inTx(ctx, func(q *db.Queries) error {
		before, err := lockEnabledUser(ctx, q, handle)
		if err != nil {
			return err
		}
		row, err := q.SetUserRole(ctx, db.SetUserRoleParams{ID: before.ID, Role: string(role)})
		if err != nil {
			return err
		}
		out = toUser(row)
		return userAudit(ctx, q, actor, auth.ActionRoleChanged, handle, userMeta(toUser(before)), userMeta(out))
	})
	return out, err
}

func (s *Postgres) DisableUser(ctx context.Context, actor, handle string) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockEnabledUser(ctx, q, handle)
		if err != nil {
			return err
		}
		if err := q.DisableUser(ctx, u.ID); err != nil {
			return err
		}
		if err := q.RevokeUserTokens(ctx, u.ID); err != nil {
			return err
		}
		return userAudit(ctx, q, actor, auth.ActionUserDisabled, handle, userMeta(toUser(u)), nil)
	})
}

func (s *Postgres) CreateToken(ctx context.Context, actor, handle, name string, hash []byte, prefix string, expiresAt *time.Time) (auth.Token, error) {
	if err := auth.ValidateToken(name, expiresAt, time.Now()); err != nil {
		return auth.Token{}, err
	}
	var out auth.Token
	err := s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockEnabledUser(ctx, q, handle)
		if err != nil {
			return err
		}
		row, err := q.CreateToken(ctx, db.CreateTokenParams{
			UserID: u.ID, Name: name, TokenHash: hash, Prefix: prefix, ExpiresAt: tsPtr(expiresAt),
		})
		if err != nil {
			return err
		}
		out = toToken(row)
		return userAudit(ctx, q, actor, auth.ActionTokenCreated, handle, nil, tokenMeta(out))
	})
	return out, err
}

func (s *Postgres) ListTokens(ctx context.Context, handle string) ([]auth.Token, error) {
	u, err := s.q.GetUser(ctx, handle)
	if err != nil {
		return nil, mapErr(err)
	}
	rows, err := s.q.ListTokens(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	out := make([]auth.Token, len(rows))
	for i, r := range rows {
		out[i] = toToken(r)
	}
	return out, nil
}

func (s *Postgres) RevokeToken(ctx context.Context, actor, handle string, id int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := q.GetUserForUpdate(ctx, handle)
		if err != nil {
			return mapErr(err)
		}
		row, err := q.RevokeToken(ctx, db.RevokeTokenParams{ID: id, UserID: u.ID})
		if err != nil {
			return mapErr(err)
		}
		return userAudit(ctx, q, actor, auth.ActionTokenRevoked, handle, tokenMeta(toToken(row)), nil)
	})
}

func (s *Postgres) Authenticate(ctx context.Context, hash []byte) (auth.User, error) {
	row, err := s.q.AuthenticateToken(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, errs.ErrUnauthorized
	}
	if err != nil {
		return auth.User{}, err
	}
	if err := s.q.TouchToken(ctx, row.TokenID); err != nil {
		return auth.User{}, err
	}
	return toUser(db.User{
		ID: row.ID, Handle: row.Handle, Name: row.Name, Role: row.Role,
		CreatedAt: row.CreatedAt, DisabledAt: row.DisabledAt,
	}), nil
}

func (s *Postgres) ListUserAuditEvents(ctx context.Context, handle string) ([]audit.Event, error) {
	rows, err := s.q.ListUserAuditEvents(ctx, &handle)
	if err != nil {
		return nil, err
	}
	return toAuditEvents(rows), nil
}

func lockEnabledUser(ctx context.Context, q *db.Queries, handle string) (db.User, error) {
	u, err := q.GetUserForUpdate(ctx, handle)
	if err != nil {
		return db.User{}, mapErr(err)
	}
	if u.DisabledAt.Valid {
		return db.User{}, fmt.Errorf("user %q is disabled: %w", handle, errs.ErrNotFound)
	}
	return u, nil
}

func userAudit(ctx context.Context, q *db.Queries, actor, action, handle string, before, after any) error {
	b, err := marshalOrNil(before)
	if err != nil {
		return err
	}
	a, err := marshalOrNil(after)
	if err != nil {
		return err
	}
	return q.InsertUserAuditEvent(ctx, db.InsertUserAuditEventParams{
		Actor: actor, Action: action, SubjectUser: &handle, Before: b, After: a,
	})
}

func userMeta(u auth.User) auth.UserMeta {
	return auth.UserMeta{Handle: u.Handle, Name: u.Name, Role: u.Role}
}

func tokenMeta(t auth.Token) auth.TokenMeta {
	return auth.TokenMeta{ID: t.ID, Name: t.Name, Prefix: t.Prefix, ExpiresAt: t.ExpiresAt}
}

func toUser(r db.User) auth.User {
	return auth.User{
		ID: r.ID, Handle: r.Handle, Name: r.Name, Role: auth.Role(r.Role),
		CreatedAt: r.CreatedAt.Time, DisabledAt: timePtr(r.DisabledAt),
	}
}

func toToken(r db.ApiToken) auth.Token {
	return auth.Token{
		ID: r.ID, Name: r.Name, Prefix: r.Prefix, CreatedAt: r.CreatedAt.Time,
		ExpiresAt: timePtr(r.ExpiresAt), LastUsedAt: timePtr(r.LastUsedAt), RevokedAt: timePtr(r.RevokedAt),
	}
}

func toAuditEvents(rows []db.AuditEvent) []audit.Event {
	out := make([]audit.Event, len(rows))
	for i, r := range rows {
		out[i] = audit.Event{
			ID:          r.ID,
			OccurredAt:  r.OccurredAt.Time,
			Actor:       r.Actor,
			Action:      r.Action,
			FlagKey:     deref(r.FlagKey),
			Environment: deref(r.Environment),
			SubjectUser: deref(r.SubjectUser),
			Before:      r.Before,
			After:       r.After,
		}
	}
	return out
}

func tsPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
