package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/eval"
	"github.com/Melmonster13/featuresteward/internal/flag"
)

type requestJSON struct {
	ID            int64              `json:"id"`
	Flag          string             `json:"flag"`
	Environment   string             `json:"environment"`
	RequestedBy   string             `json:"requested_by"`
	Reason        string             `json:"reason"`
	Base          flag.EnvConfig     `json:"base"`
	Proposed      flag.EnvConfig     `json:"proposed"`
	Status        flag.RequestStatus `json:"status"`
	ReviewedBy    *string            `json:"reviewed_by"` // null until reviewed
	ReviewComment string             `json:"review_comment"`
	CreatedAt     time.Time          `json:"created_at"`
	ExpiresAt     time.Time          `json:"expires_at"`
	ResolvedAt    *time.Time         `json:"resolved_at,omitempty"`
}

func toRequestJSON(r flag.ChangeRequest) requestJSON {
	out := requestJSON{
		ID: r.ID, Flag: r.FlagKey, Environment: r.Environment, RequestedBy: r.RequestedBy, Reason: r.Reason,
		Base: r.Base, Proposed: r.Proposed, Status: r.Status, ReviewComment: r.ReviewComment,
		CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, ResolvedAt: r.ResolvedAt,
	}
	if r.ReviewedBy != "" {
		out.ReviewedBy = &r.ReviewedBy
	}
	return out
}

// createRequest proposes a change to a flag in a protected environment.
func (s *server) createRequest(w http.ResponseWriter, r *http.Request) {
	env, err := s.flags.GetEnvironment(r.Context(), r.PathValue("env"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !env.Protected {
		writeError(w, http.StatusBadRequest, env.Name+" isn't protected; change the flag there directly")
		return
	}
	var req struct {
		Enabled           *bool       `json:"enabled"`
		RolloutPercentage *int        `json:"rollout_percentage"`
		Rules             []eval.Rule `json:"rules"`
		Reason            string      `json:"reason"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Enabled == nil || req.RolloutPercentage == nil {
		writeError(w, http.StatusBadRequest, "enabled and rollout_percentage are required")
		return
	}
	cfg := flag.EnvConfig{Enabled: *req.Enabled, RolloutPercentage: *req.RolloutPercentage, Rules: req.Rules}
	cr, err := s.flags.CreateChangeRequest(r.Context(), actor(r), r.PathValue("key"), env.Key, cfg,
		strings.TrimSpace(req.Reason), time.Now().Add(flag.RequestTTL))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/requests/"+strconv.FormatInt(cr.ID, 10))
	writeJSON(w, http.StatusCreated, toRequestJSON(cr))
}

func (s *server) listRequests(w http.ResponseWriter, r *http.Request) {
	filter := flag.RequestFilter{Status: flag.RequestStatus(r.URL.Query().Get("status")), FlagKey: r.URL.Query().Get("flag")}
	switch filter.Status {
	case "", flag.RequestPending, flag.RequestApproved, flag.RequestRejected, flag.RequestCancelled, flag.RequestExpired:
	default:
		writeError(w, http.StatusBadRequest, "status must be pending, approved, rejected, cancelled, or expired")
		return
	}
	rs, err := s.flags.ListChangeRequests(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]requestJSON, len(rs))
	for i, cr := range rs {
		out[i] = toRequestJSON(cr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func (s *server) getRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cr, err := s.flags.GetChangeRequest(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRequestJSON(cr))
}

func (s *server) approveRequest(w http.ResponseWriter, r *http.Request) {
	s.review(w, r, s.flags.ApproveChangeRequest)
}

func (s *server) rejectRequest(w http.ResponseWriter, r *http.Request) {
	s.review(w, r, s.flags.RejectChangeRequest)
}

// review approves or rejects a request. Reviewers are approvers, admins,
// and the flag's steward; the store also refuses the requester.
func (s *server) review(w http.ResponseWriter, r *http.Request,
	act func(ctx context.Context, actor string, id int64, comment string) (flag.ChangeRequest, error)) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Comment string `json:"comment"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	cr, err := s.flags.GetChangeRequest(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	me := principalFrom(r).user
	if !me.Role.AtLeast(auth.RoleApprover) {
		f, err := s.flags.GetFlag(r.Context(), cr.FlagKey)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if f.Steward != me.Handle {
			writeError(w, http.StatusForbidden, "only the flag's steward, an approver, or an admin can review this request")
			return
		}
	}
	cr, err = act(r.Context(), actor(r), id, strings.TrimSpace(req.Comment))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRequestJSON(cr))
}

func (s *server) cancelRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cr, err := s.flags.CancelChangeRequest(r.Context(), actor(r), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRequestJSON(cr))
}
