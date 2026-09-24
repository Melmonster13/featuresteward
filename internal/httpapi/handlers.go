package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/errs"
	"github.com/Melmonster13/featuresteward/internal/eval"
	"github.com/Melmonster13/featuresteward/internal/flag"
)

type flagJSON struct {
	Key          string                    `json:"key"`
	Name         string                    `json:"name"`
	Description  string                    `json:"description"`
	Steward      *string                   `json:"steward"` // null when unassigned
	CreatedAt    time.Time                 `json:"created_at"`
	UpdatedAt    time.Time                 `json:"updated_at"`
	ArchivedAt   *time.Time                `json:"archived_at,omitempty"`
	Environments map[string]flag.EnvConfig `json:"environments"`
}

func toFlagJSON(f flag.Flag) flagJSON {
	return flagJSON{
		Key: f.Key, Name: f.Name, Description: f.Description, Steward: flag.NewStewardSnapshot(f.Steward).Steward,
		CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt, ArchivedAt: f.ArchivedAt,
		Environments: f.Environments,
	}
}

type auditEventJSON struct {
	ID          int64           `json:"id"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Actor       string          `json:"actor"`
	Action      string          `json:"action"`
	Environment string          `json:"environment,omitempty"`
	Before      json.RawMessage `json:"before"`
	After       json.RawMessage `json:"after"`
}

func (s *server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	envs, err := s.flags.ListEnvironments(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"environments": envs})
}

// listFlags accepts ?steward=<handle>, or ?steward=none for flags whose
// steward is unset or disabled.
func (s *server) listFlags(w http.ResponseWriter, r *http.Request) {
	flags, err := s.flags.ListFlags(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	match := func(flag.Flag) bool { return true }
	switch steward := r.URL.Query().Get("steward"); steward {
	case "":
	case "none":
		disabled, err := s.disabledHandles(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		match = func(f flag.Flag) bool { return f.Steward == "" || disabled[f.Steward] }
	default:
		match = func(f flag.Flag) bool { return f.Steward == steward }
	}
	out := []flagJSON{}
	for _, f := range flags {
		if match(f) {
			out = append(out, toFlagJSON(f))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"flags": out})
}

func (s *server) disabledHandles(r *http.Request) (map[string]bool, error) {
	users, err := s.users.ListUsers(r.Context())
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, u := range users {
		if u.DisabledAt != nil {
			out[u.Handle] = true
		}
	}
	return out, nil
}

// checkSteward returns an error unless handle is an active editor or above.
func (s *server) checkSteward(r *http.Request, handle string) error {
	u, err := s.users.GetUser(r.Context(), handle)
	if err == nil && u.DisabledAt == nil && u.Role.AtLeast(auth.RoleEditor) {
		return nil
	}
	if err != nil && !errors.Is(err, errs.ErrNotFound) {
		return err
	}
	return errs.Invalid("steward must be an active user with the editor role or higher")
}

// setSteward lets an admin, or the flag's current steward, reassign it.
func (s *server) setSteward(w http.ResponseWriter, r *http.Request) {
	f, err := s.flags.GetFlag(r.Context(), r.PathValue("key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	me := principalFrom(r).user
	if !me.Role.AtLeast(auth.RoleAdmin) && (f.Steward == "" || f.Steward != me.Handle) {
		writeError(w, http.StatusForbidden, "only an admin or the flag's current steward can reassign it")
		return
	}
	var req struct {
		Steward string `json:"steward"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.checkSteward(r, req.Steward); err != nil {
		s.fail(w, r, err)
		return
	}
	f, err = s.flags.SetSteward(r.Context(), actor(r), f.Key, req.Steward)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toFlagJSON(f))
}

func (s *server) createFlag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Steward     string `json:"steward"` // defaults to the creator
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Steward == "" {
		req.Steward = actor(r)
	} else if err := s.checkSteward(r, req.Steward); err != nil {
		s.fail(w, r, err)
		return
	}
	f, err := s.flags.CreateFlag(r.Context(), actor(r), req.Key, req.Name, req.Description, req.Steward)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/flags/"+f.Key)
	writeJSON(w, http.StatusCreated, toFlagJSON(f))
}

func (s *server) getFlag(w http.ResponseWriter, r *http.Request) {
	f, err := s.flags.GetFlag(r.Context(), r.PathValue("key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toFlagJSON(f))
}

func (s *server) updateFlag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decode(w, r, &req) {
		return
	}
	f, err := s.flags.UpdateFlag(r.Context(), actor(r), r.PathValue("key"), req.Name, req.Description)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toFlagJSON(f))
}

func (s *server) archiveFlag(w http.ResponseWriter, r *http.Request) {
	if err := s.flags.ArchiveFlag(r.Context(), actor(r), r.PathValue("key")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) updateEnvironment(w http.ResponseWriter, r *http.Request) {
	env, err := s.flags.GetEnvironment(r.Context(), r.PathValue("env"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Until approvals exist (Milestone 5), only admins change protected environments.
	if env.Protected && !principalFrom(r).user.Role.AtLeast(auth.RoleAdmin) {
		writeError(w, http.StatusForbidden, "changes to protected environment "+env.Key+" need the admin role")
		return
	}
	var req struct {
		Enabled           *bool       `json:"enabled"`
		RolloutPercentage *int        `json:"rollout_percentage"`
		Rules             []eval.Rule `json:"rules"`
	}
	if !decode(w, r, &req) {
		return
	}
	// Required so a partial body can't silently reset a flag to defaults.
	if req.Enabled == nil || req.RolloutPercentage == nil {
		writeError(w, http.StatusBadRequest, "enabled and rollout_percentage are required")
		return
	}
	cfg := flag.EnvConfig{Enabled: *req.Enabled, RolloutPercentage: *req.RolloutPercentage, Rules: req.Rules}
	f, err := s.flags.UpdateEnvironment(r.Context(), actor(r), r.PathValue("key"), env.Key, cfg)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toFlagJSON(f))
}

func (s *server) listAudit(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if _, err := s.flags.GetFlag(r.Context(), key); err != nil {
		s.fail(w, r, err)
		return
	}
	events, err := s.flags.ListAuditEvents(r.Context(), key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": toAuditJSON(events)})
}

func (s *server) evaluate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Flag        string   `json:"flag"`
		Environment string   `json:"environment"`
		UserID      string   `json:"user_id"`
		Groups      []string `json:"groups"`
	}
	if !decode(w, r, &req) {
		return
	}
	// An SDK key is tied to one environment, which is the default.
	if env := principalFrom(r).sdkEnv; env != "" {
		if req.Environment == "" {
			req.Environment = env
		} else if req.Environment != env {
			writeError(w, http.StatusForbidden, "this SDK key is for environment "+env)
			return
		}
	}
	if req.Flag == "" || req.Environment == "" {
		writeError(w, http.StatusBadRequest, "flag and environment are required")
		return
	}
	cfg, err := s.flags.EvalConfig(r.Context(), req.Flag, req.Environment)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res := eval.Evaluate(cfg, eval.Context{UserID: req.UserID, Groups: req.Groups})
	writeJSON(w, http.StatusOK, map[string]any{"flag": req.Flag, "enabled": res.Enabled, "reason": res.Reason})
}
