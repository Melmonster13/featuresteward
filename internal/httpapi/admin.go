package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Melmonster13/featuresteward/internal/audit"
	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/errs"
	"github.com/Melmonster13/featuresteward/internal/flag"
)

type userJSON struct {
	Handle     string     `json:"handle"`
	Name       string     `json:"name"`
	Role       auth.Role  `json:"role"`
	CreatedAt  time.Time  `json:"created_at"`
	DisabledAt *time.Time `json:"disabled_at,omitempty"`
}

func toUserJSON(u auth.User) userJSON {
	return userJSON{Handle: u.Handle, Name: u.Name, Role: u.Role, CreatedAt: u.CreatedAt, DisabledAt: u.DisabledAt}
}

type tokenJSON struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	// Token is the secret, returned only when the token is created.
	Token string `json:"token,omitempty"`
}

func toTokenJSON(t auth.Token) tokenJSON {
	return tokenJSON{ID: t.ID, Name: t.Name, Prefix: t.Prefix, CreatedAt: t.CreatedAt,
		ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt}
}

type sdkKeyJSON struct {
	ID          int64      `json:"id"`
	Environment string     `json:"environment"`
	Name        string     `json:"name"`
	Prefix      string     `json:"prefix"`
	CreatedAt   time.Time  `json:"created_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	// Key is the secret, returned only when the key is created.
	Key string `json:"key,omitempty"`
}

func toSDKKeyJSON(k auth.SDKKey) sdkKeyJSON {
	return sdkKeyJSON{ID: k.ID, Environment: k.Environment, Name: k.Name, Prefix: k.Prefix,
		CreatedAt: k.CreatedAt, RevokedAt: k.RevokedAt}
}

func toAuditJSON(events []audit.Event) []auditEventJSON {
	out := make([]auditEventJSON, len(events))
	for i, e := range events {
		out[i] = auditEventJSON{
			ID: e.ID, OccurredAt: e.OccurredAt, Actor: e.Actor, Action: e.Action,
			Environment: e.Environment, Before: e.Before, After: e.After,
		}
	}
	return out
}

// --- Current user ---

func (s *server) getMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, toUserJSON(*principalFrom(r).user))
}

func (s *server) listMyTokens(w http.ResponseWriter, r *http.Request) {
	s.listTokensFor(w, r, actor(r))
}

func (s *server) createMyToken(w http.ResponseWriter, r *http.Request) {
	s.createTokenFor(w, r, actor(r))
}

func (s *server) revokeMyToken(w http.ResponseWriter, r *http.Request) {
	s.revokeTokenFor(w, r, actor(r))
}

// --- Users (admin) ---

func (s *server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.users.ListUsers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]userJSON, len(users))
	for i, u := range users {
		out[i] = toUserJSON(u)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Handle string    `json:"handle"`
		Name   string    `json:"name"`
		Role   auth.Role `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	u, err := s.users.CreateUser(r.Context(), actor(r), req.Handle, req.Name, req.Role)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/users/"+u.Handle)
	writeJSON(w, http.StatusCreated, toUserJSON(u))
}

func (s *server) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.users.GetUser(r.Context(), r.PathValue("handle"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserJSON(u))
}

// notSelf refuses admin actions on the caller's own account. Since the
// caller must be an admin, this also guarantees an admin always remains.
func notSelf(w http.ResponseWriter, r *http.Request) bool {
	if r.PathValue("handle") == actor(r) {
		writeError(w, http.StatusForbidden, "you can't change your own role or disable yourself; ask another admin")
		return false
	}
	return true
}

func (s *server) setRole(w http.ResponseWriter, r *http.Request) {
	if !notSelf(w, r) {
		return
	}
	var req struct {
		Role auth.Role `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	u, err := s.users.SetRole(r.Context(), actor(r), r.PathValue("handle"), req.Role)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserJSON(u))
}

func (s *server) disableUser(w http.ResponseWriter, r *http.Request) {
	if !notSelf(w, r) {
		return
	}
	if err := s.users.DisableUser(r.Context(), actor(r), r.PathValue("handle")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listUserAudit(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if _, err := s.users.GetUser(r.Context(), handle); err != nil {
		s.fail(w, r, err)
		return
	}
	events, err := s.users.ListUserAuditEvents(r.Context(), handle)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": toAuditJSON(events)})
}

func (s *server) listUserTokens(w http.ResponseWriter, r *http.Request) {
	s.listTokensFor(w, r, r.PathValue("handle"))
}

// createUserToken lets an admin issue a token to hand to a new user.
func (s *server) createUserToken(w http.ResponseWriter, r *http.Request) {
	s.createTokenFor(w, r, r.PathValue("handle"))
}

func (s *server) revokeUserToken(w http.ResponseWriter, r *http.Request) {
	s.revokeTokenFor(w, r, r.PathValue("handle"))
}

// --- Token helpers ---

func (s *server) listTokensFor(w http.ResponseWriter, r *http.Request, handle string) {
	toks, err := s.users.ListTokens(r.Context(), handle)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]tokenJSON, len(toks))
	for i, t := range toks {
		out[i] = toTokenJSON(t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (s *server) createTokenFor(w http.ResponseWriter, r *http.Request, handle string) {
	var req struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if !decode(w, r, &req) {
		return
	}
	secret, hash, prefix := auth.NewSecret(auth.TokenPrefix)
	t, err := s.users.CreateToken(r.Context(), actor(r), handle, req.Name, hash, prefix, req.ExpiresAt)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := toTokenJSON(t)
	out.Token = secret
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, out)
}

func (s *server) revokeTokenFor(w http.ResponseWriter, r *http.Request, handle string) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.users.RevokeToken(r.Context(), actor(r), handle, id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not found")
		return 0, false
	}
	return id, true
}

// --- SDK keys (admin) ---

func (s *server) listSDKKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.users.ListSDKKeys(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]sdkKeyJSON, len(keys))
	for i, k := range keys {
		out[i] = toSDKKeyJSON(k)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sdk_keys": out})
}

func (s *server) createSDKKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Environment string `json:"environment"`
		Name        string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	secret, hash, prefix := auth.NewSecret(auth.SDKKeyPrefix)
	k, err := s.users.CreateSDKKey(r.Context(), actor(r), req.Environment, req.Name, hash, prefix)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := toSDKKeyJSON(k)
	out.Key = secret
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, out)
}

func (s *server) revokeSDKKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.users.RevokeSDKKey(r.Context(), actor(r), id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Environments (admin) ---

func (s *server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var req flag.Environment
	if !decode(w, r, &req) {
		return
	}
	env, err := s.flags.CreateEnvironment(r.Context(), actor(r), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, env)
}

func (s *server) updateEnvironmentSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		Protected *bool  `json:"protected"`
	}
	if !decode(w, r, &req) {
		return
	}
	// Required so omitting it can't silently unprotect an environment.
	if req.Protected == nil {
		s.fail(w, r, errs.Invalid("protected is required"))
		return
	}
	env, err := s.flags.UpdateEnvironmentSettings(r.Context(), actor(r),
		flag.Environment{Key: r.PathValue("key"), Name: req.Name, Protected: *req.Protected})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
}
