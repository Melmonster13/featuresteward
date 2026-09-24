package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Melmonster13/featuresteward/internal/audit"
	"github.com/Melmonster13/featuresteward/internal/errs"
	"github.com/Melmonster13/featuresteward/internal/eval"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/store/db"
)

func (s *Postgres) CreateChangeRequest(ctx context.Context, actor, key, env string, cfg flag.EnvConfig, reason string, expiresAt time.Time) (flag.ChangeRequest, error) {
	if err := cfg.Validate(); err != nil {
		return flag.ChangeRequest{}, err
	}
	if cfg.Rules == nil {
		cfg.Rules = []eval.Rule{}
	}
	var out flag.ChangeRequest
	err := s.inTx(ctx, func(q *db.Queries) error {
		f, err := lockActive(ctx, q, key)
		if err != nil {
			return err
		}
		cur, err := q.GetFlagEnvironmentForUpdate(ctx, db.GetFlagEnvironmentForUpdateParams{FlagID: f.ID, Environment: env})
		if err != nil {
			return mapErr(err)
		}
		base, err := toEnvConfig(cur.Enabled, cur.RolloutPercentage, cur.Rules)
		if err != nil {
			return err
		}
		if cfg.Equal(base) {
			return errs.Invalid("the request doesn't change anything")
		}
		// An expired request mustn't block a new one until cleanup runs.
		if err := expire(ctx, q, &f.ID, &env); err != nil {
			return err
		}
		baseJSON, err := json.Marshal(base)
		if err != nil {
			return err
		}
		proposedJSON, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		id, err := q.InsertChangeRequest(ctx, db.InsertChangeRequestParams{
			FlagID: f.ID, Environment: env, RequestedBy: actor, Reason: reason,
			Base: baseJSON, Proposed: proposedJSON, ExpiresAt: tsPtr(&expiresAt),
		})
		if err != nil {
			if err := mapErr(err); errors.Is(err, errs.ErrConflict) {
				return errs.Conflict("a change request is already pending for this flag in " + env)
			}
			return mapErr(err)
		}
		if out, err = getRequest(ctx, q, id); err != nil {
			return err
		}
		return flagAudit(ctx, q, actor, flag.ActionRequestCreated, key, env, nil, flag.NewRequestSnapshot(out))
	})
	return out, err
}

func (s *Postgres) GetChangeRequest(ctx context.Context, id int64) (flag.ChangeRequest, error) {
	return getRequest(ctx, s.q, id)
}

func (s *Postgres) ListChangeRequests(ctx context.Context, filter flag.RequestFilter) ([]flag.ChangeRequest, error) {
	rows, err := s.q.ListChangeRequests(ctx, db.ListChangeRequestsParams{
		Status: strPtr(string(filter.Status)), FlagKey: strPtr(filter.FlagKey),
	})
	if err != nil {
		return nil, err
	}
	out := make([]flag.ChangeRequest, len(rows))
	for i, r := range rows {
		if out[i], err = toRequest(db.GetChangeRequestRow(r)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Postgres) ApproveChangeRequest(ctx context.Context, actor string, id int64, comment string) (flag.ChangeRequest, error) {
	var out flag.ChangeRequest
	err := s.inTx(ctx, func(q *db.Queries) error {
		r, err := lockPending(ctx, q, id)
		if err != nil {
			return err
		}
		if actor == r.RequestedBy {
			return errs.Forbidden("you can't approve your own change request")
		}
		if !r.ExpiresAt.After(time.Now()) {
			return errs.Conflict("this change request has expired")
		}
		f, err := lockActive(ctx, q, r.FlagKey)
		if err != nil {
			return err
		}
		cur, err := q.GetFlagEnvironmentForUpdate(ctx, db.GetFlagEnvironmentForUpdateParams{FlagID: f.ID, Environment: r.Environment})
		if err != nil {
			return mapErr(err)
		}
		current, err := toEnvConfig(cur.Enabled, cur.RolloutPercentage, cur.Rules)
		if err != nil {
			return err
		}
		if !current.Equal(r.Base) {
			return errs.Conflict(r.Environment + " has changed since this request was made; ask for a new request")
		}
		if err := writeEnv(ctx, q, actor, f, r.Environment, r.Base, r.Proposed, ""); err != nil {
			return err
		}
		out, err = resolve(ctx, q, r, actor, flag.RequestApproved, comment, flag.ActionRequestApproved)
		return err
	})
	return out, err
}

func (s *Postgres) RejectChangeRequest(ctx context.Context, actor string, id int64, comment string) (flag.ChangeRequest, error) {
	var out flag.ChangeRequest
	err := s.inTx(ctx, func(q *db.Queries) error {
		r, err := lockPending(ctx, q, id)
		if err != nil {
			return err
		}
		if actor == r.RequestedBy {
			return errs.Forbidden("cancel your own change request instead of rejecting it")
		}
		out, err = resolve(ctx, q, r, actor, flag.RequestRejected, comment, flag.ActionRequestRejected)
		return err
	})
	return out, err
}

func (s *Postgres) CancelChangeRequest(ctx context.Context, actor string, id int64) (flag.ChangeRequest, error) {
	var out flag.ChangeRequest
	err := s.inTx(ctx, func(q *db.Queries) error {
		r, err := lockPending(ctx, q, id)
		if err != nil {
			return err
		}
		if actor != r.RequestedBy {
			return errs.Forbidden("only the requester can cancel a change request")
		}
		out, err = resolve(ctx, q, r, actor, flag.RequestCancelled, "", flag.ActionRequestCancelled)
		return err
	})
	return out, err
}

func (s *Postgres) ExpireChangeRequests(ctx context.Context) (int, error) {
	n := 0
	err := s.inTx(ctx, func(q *db.Queries) error {
		rows, err := q.ExpireChangeRequests(ctx, db.ExpireChangeRequestsParams{})
		n = len(rows)
		if err != nil {
			return err
		}
		return auditExpired(ctx, q, rows)
	})
	return n, err
}

// expire marks pending requests for one flag and environment as expired
// once they're past their expiry.
func expire(ctx context.Context, q *db.Queries, flagID *int64, env *string) error {
	rows, err := q.ExpireChangeRequests(ctx, db.ExpireChangeRequestsParams{FlagID: flagID, Environment: env})
	if err != nil {
		return err
	}
	return auditExpired(ctx, q, rows)
}

func auditExpired(ctx context.Context, q *db.Queries, rows []db.ExpireChangeRequestsRow) error {
	for _, r := range rows {
		var proposed flag.EnvConfig
		if err := json.Unmarshal(r.Proposed, &proposed); err != nil {
			return err
		}
		snap := flag.RequestSnapshot{ID: r.ID, Status: flag.RequestExpired, RequestedBy: r.RequestedBy, Proposed: proposed, Reason: r.Reason}
		if err := flagAudit(ctx, q, audit.SystemActor, flag.ActionRequestExpired, r.FlagKey, r.Environment, nil, snap); err != nil {
			return err
		}
	}
	return nil
}

// lockPending locks a request for the rest of the transaction and
// rejects closed ones.
func lockPending(ctx context.Context, q *db.Queries, id int64) (flag.ChangeRequest, error) {
	row, err := q.GetChangeRequestForUpdate(ctx, id)
	if err != nil {
		return flag.ChangeRequest{}, mapErr(err)
	}
	r, err := toRequest(db.GetChangeRequestRow(row))
	if err != nil {
		return flag.ChangeRequest{}, err
	}
	if r.Status != flag.RequestPending {
		return flag.ChangeRequest{}, errs.Conflict("this change request is already " + string(r.Status))
	}
	return r, nil
}

func resolve(ctx context.Context, q *db.Queries, r flag.ChangeRequest, actor string, status flag.RequestStatus, comment, action string) (flag.ChangeRequest, error) {
	n, err := q.ResolveChangeRequest(ctx, db.ResolveChangeRequestParams{
		ID: r.ID, Status: string(status), ReviewedBy: &actor, ReviewComment: comment,
	})
	if err != nil {
		return flag.ChangeRequest{}, err
	}
	if n != 1 { // lockPending should make this impossible
		return flag.ChangeRequest{}, errs.Conflict("this change request was already closed")
	}
	out, err := getRequest(ctx, q, r.ID)
	if err != nil {
		return flag.ChangeRequest{}, err
	}
	return out, flagAudit(ctx, q, actor, action, r.FlagKey, r.Environment, nil, flag.NewRequestSnapshot(out))
}

func getRequest(ctx context.Context, q *db.Queries, id int64) (flag.ChangeRequest, error) {
	row, err := q.GetChangeRequest(ctx, id)
	if err != nil {
		return flag.ChangeRequest{}, mapErr(err)
	}
	return toRequest(row)
}

func toRequest(r db.GetChangeRequestRow) (flag.ChangeRequest, error) {
	out := flag.ChangeRequest{
		ID: r.ID, FlagKey: r.FlagKey, Environment: r.Environment, RequestedBy: r.RequestedBy, Reason: r.Reason,
		Status: flag.RequestStatus(r.Status), ReviewedBy: deref(r.ReviewedBy), ReviewComment: r.ReviewComment,
		CreatedAt: r.CreatedAt.Time, ExpiresAt: r.ExpiresAt.Time, ResolvedAt: timePtr(r.ResolvedAt),
	}
	if err := json.Unmarshal(r.Base, &out.Base); err != nil {
		return out, err
	}
	if err := json.Unmarshal(r.Proposed, &out.Proposed); err != nil {
		return out, err
	}
	return out, nil
}
