package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Melmonster13/featuresteward/internal/eval"
	"github.com/Melmonster13/featuresteward/internal/flag"
)

type flagJSON struct {
	Key          string                    `json:"key"`
	Name         string                    `json:"name"`
	Description  string                    `json:"description"`
	CreatedAt    time.Time                 `json:"created_at"`
	UpdatedAt    time.Time                 `json:"updated_at"`
	ArchivedAt   *time.Time                `json:"archived_at,omitempty"`
	Environments map[string]flag.EnvConfig `json:"environments"`
}

func toFlagJSON(f flag.Flag) flagJSON {
	return flagJSON{
		Key: f.Key, Name: f.Name, Description: f.Description,
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
	envs, err := s.store.ListEnvironments(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"environments": envs})
}

func (s *server) listFlags(w http.ResponseWriter, r *http.Request) {
	flags, err := s.store.ListFlags(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]flagJSON, len(flags))
	for i, f := range flags {
		out[i] = toFlagJSON(f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"flags": out})
}

func (s *server) createFlag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decode(w, r, &req) {
		return
	}
	f, err := s.store.CreateFlag(r.Context(), actor, req.Key, req.Name, req.Description)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/flags/"+f.Key)
	writeJSON(w, http.StatusCreated, toFlagJSON(f))
}

func (s *server) getFlag(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.GetFlag(r.Context(), r.PathValue("key"))
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
	f, err := s.store.UpdateFlag(r.Context(), actor, r.PathValue("key"), req.Name, req.Description)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toFlagJSON(f))
}

func (s *server) archiveFlag(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ArchiveFlag(r.Context(), actor, r.PathValue("key")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) updateEnvironment(w http.ResponseWriter, r *http.Request) {
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
	f, err := s.store.UpdateEnvironment(r.Context(), actor, r.PathValue("key"), r.PathValue("env"), cfg)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toFlagJSON(f))
}

func (s *server) listAudit(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if _, err := s.store.GetFlag(r.Context(), key); err != nil {
		s.fail(w, r, err)
		return
	}
	events, err := s.store.ListAuditEvents(r.Context(), key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]auditEventJSON, len(events))
	for i, e := range events {
		out[i] = auditEventJSON{
			ID: e.ID, OccurredAt: e.OccurredAt, Actor: e.Actor, Action: e.Action,
			Environment: e.Environment, Before: e.Before, After: e.After,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
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
	if req.Flag == "" || req.Environment == "" {
		writeError(w, http.StatusBadRequest, "flag and environment are required")
		return
	}
	cfg, err := s.store.EvalConfig(r.Context(), req.Flag, req.Environment)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res := eval.Evaluate(cfg, eval.Context{UserID: req.UserID, Groups: req.Groups})
	writeJSON(w, http.StatusOK, map[string]any{"flag": req.Flag, "enabled": res.Enabled, "reason": res.Reason})
}
