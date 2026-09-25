package flagtest

import (
	"context"
	"sort"
	"time"

	"github.com/Melmonster13/featuresteward/internal/audit"
	"github.com/Melmonster13/featuresteward/internal/errs"
	"github.com/Melmonster13/featuresteward/internal/flag"
)

func (m *Memory) CreateChangeRequest(_ context.Context, actor, key, env string, cfg flag.EnvConfig, reason string, expiresAt time.Time) (flag.ChangeRequest, error) {
	if err := cfg.Validate(); err != nil {
		return flag.ChangeRequest{}, err
	}
	cfg.Rules = cloneRules(cfg.Rules)
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.active(key)
	if err != nil {
		return flag.ChangeRequest{}, err
	}
	base, ok := f.Environments[env]
	if !ok {
		return flag.ChangeRequest{}, flag.ErrNotFound
	}
	if cfg.Equal(base) {
		return flag.ChangeRequest{}, errs.Invalid("the request doesn't change anything")
	}
	m.expire(func(r *flag.ChangeRequest) bool { return r.FlagKey == key && r.Environment == env })
	for _, r := range m.requests {
		if r.FlagKey == key && r.Environment == env && r.Status == flag.RequestPending {
			return flag.ChangeRequest{}, errs.Conflict("a change request is already pending for this flag in " + env)
		}
	}
	r := &flag.ChangeRequest{
		ID: int64(len(m.requests) + 1), FlagKey: key, Environment: env, RequestedBy: actor, Reason: reason,
		Base: cloneConfig(base), Proposed: cfg, Status: flag.RequestPending, CreatedAt: time.Now(), ExpiresAt: expiresAt,
	}
	m.requests = append(m.requests, r)
	m.audit(actor, flag.ActionRequestCreated, key, env, nil, flag.NewRequestSnapshot(*r))
	return cloneRequest(r), nil
}

func (m *Memory) GetChangeRequest(_ context.Context, id int64) (flag.ChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.request(id)
	if err != nil {
		return flag.ChangeRequest{}, err
	}
	return cloneRequest(r), nil
}

func (m *Memory) ListChangeRequests(_ context.Context, filter flag.RequestFilter) ([]flag.ChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []flag.ChangeRequest{}
	for _, r := range m.requests {
		if (filter.Status == "" || r.Status == filter.Status) && (filter.FlagKey == "" || r.FlagKey == filter.FlagKey) {
			out = append(out, cloneRequest(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (m *Memory) ApproveChangeRequest(_ context.Context, actor string, id int64, comment string) (flag.ChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.pending(id)
	if err != nil {
		return flag.ChangeRequest{}, err
	}
	if actor == r.RequestedBy {
		return flag.ChangeRequest{}, errs.Forbidden("you can't approve your own change request")
	}
	if !r.ExpiresAt.After(time.Now()) {
		return flag.ChangeRequest{}, errs.Conflict("this change request has expired")
	}
	f, err := m.active(r.FlagKey)
	if err != nil {
		return flag.ChangeRequest{}, err
	}
	if !f.Environments[r.Environment].Equal(r.Base) {
		return flag.ChangeRequest{}, errs.Conflict(r.Environment + " has changed since this request was made; ask for a new request")
	}
	f.Environments[r.Environment] = cloneConfig(r.Proposed)
	f.UpdatedAt = time.Now()
	f.Activity[r.Environment] = flag.Activity{ChangedAt: f.UpdatedAt, EvaluatedAt: f.Activity[r.Environment].EvaluatedAt}
	m.audit(actor, flag.ActionEnvUpdated, r.FlagKey, r.Environment, r.Base, r.Proposed)
	m.resolve(r, actor, flag.RequestApproved, comment, flag.ActionRequestApproved)
	return cloneRequest(r), nil
}

func (m *Memory) RejectChangeRequest(_ context.Context, actor string, id int64, comment string) (flag.ChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.pending(id)
	if err != nil {
		return flag.ChangeRequest{}, err
	}
	if actor == r.RequestedBy {
		return flag.ChangeRequest{}, errs.Forbidden("cancel your own change request instead of rejecting it")
	}
	m.resolve(r, actor, flag.RequestRejected, comment, flag.ActionRequestRejected)
	return cloneRequest(r), nil
}

func (m *Memory) CancelChangeRequest(_ context.Context, actor string, id int64) (flag.ChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.pending(id)
	if err != nil {
		return flag.ChangeRequest{}, err
	}
	if actor != r.RequestedBy {
		return flag.ChangeRequest{}, errs.Forbidden("only the requester can cancel a change request")
	}
	m.resolve(r, actor, flag.RequestCancelled, "", flag.ActionRequestCancelled)
	return cloneRequest(r), nil
}

func (m *Memory) ExpireChangeRequests(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.expire(func(*flag.ChangeRequest) bool { return true }), nil
}

// expire marks matching pending requests past their expiry as expired.
func (m *Memory) expire(match func(*flag.ChangeRequest) bool) int {
	n := 0
	now := time.Now()
	for _, r := range m.requests {
		if r.Status == flag.RequestPending && !r.ExpiresAt.After(now) && match(r) {
			m.resolve(r, audit.SystemActor, flag.RequestExpired, "", flag.ActionRequestExpired)
			r.ReviewedBy = ""
			n++
		}
	}
	return n
}

func (m *Memory) resolve(r *flag.ChangeRequest, actor string, status flag.RequestStatus, comment, action string) {
	now := time.Now()
	r.Status, r.ReviewedBy, r.ReviewComment, r.ResolvedAt = status, actor, comment, &now
	m.audit(actor, action, r.FlagKey, r.Environment, nil, flag.NewRequestSnapshot(*r))
}

func (m *Memory) request(id int64) (*flag.ChangeRequest, error) {
	for _, r := range m.requests {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, flag.ErrNotFound
}

func (m *Memory) pending(id int64) (*flag.ChangeRequest, error) {
	r, err := m.request(id)
	if err != nil {
		return nil, err
	}
	if r.Status != flag.RequestPending {
		return nil, errs.Conflict("this change request is already " + string(r.Status))
	}
	return r, nil
}

func cloneRequest(r *flag.ChangeRequest) flag.ChangeRequest {
	c := *r
	c.Base, c.Proposed = cloneConfig(r.Base), cloneConfig(r.Proposed)
	return c
}

func cloneConfig(c flag.EnvConfig) flag.EnvConfig {
	c.Rules = cloneRules(c.Rules)
	return c
}
