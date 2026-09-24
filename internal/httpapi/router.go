// Package httpapi serves the FeatureSteward REST API.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/errs"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/idempotency"
)

const maxBodyBytes = 1 << 20

type server struct {
	flags flag.Store
	users auth.Store
	idem  idempotency.Store
	log   *slog.Logger
}

// NewRouter returns the API handler. Every /api/v1 route needs
// "Authorization: Bearer <credential>": a user's API token, or for
// POST /api/v1/evaluate only, an SDK key.
//
// Each route names the minimum role it needs. Roles are cumulative, and
// every user is at least a viewer. State-changing routes accept an
// Idempotency-Key header.
func NewRouter(flags flag.Store, users auth.Store, idem idempotency.Store, log *slog.Logger) http.Handler {
	s := &server{flags: flags, users: users, idem: idem, log: log}

	api := http.NewServeMux()
	register := func(pattern string, min auth.Role, redact bool, h http.HandlerFunc) {
		var handler http.Handler = h
		if !strings.HasPrefix(pattern, "GET ") {
			handler = s.idempotent(redact, h)
		}
		api.Handle(pattern, requireRole(min, handler))
	}
	route := func(pattern string, min auth.Role, h http.HandlerFunc) { register(pattern, min, false, h) }
	// secretRoute is for routes whose response includes a new secret.
	secretRoute := func(pattern string, min auth.Role, h http.HandlerFunc) { register(pattern, min, true, h) }
	route("GET /api/v1/environments", auth.RoleViewer, s.listEnvironments)
	route("GET /api/v1/flags", auth.RoleViewer, s.listFlags)
	route("GET /api/v1/flags/{key}", auth.RoleViewer, s.getFlag)
	route("GET /api/v1/flags/{key}/audit", auth.RoleViewer, s.listAudit)
	route("POST /api/v1/flags", auth.RoleEditor, s.createFlag)
	route("PUT /api/v1/flags/{key}", auth.RoleEditor, s.updateFlag)
	// Protected environments additionally need an admin; see updateEnvironment.
	route("PUT /api/v1/flags/{key}/environments/{env}", auth.RoleEditor, s.updateEnvironment)
	// Archiving turns a flag off everywhere, including protected environments.
	route("DELETE /api/v1/flags/{key}", auth.RoleAdmin, s.archiveFlag)
	// Open to any user or SDK key.
	api.HandleFunc("POST /api/v1/evaluate", s.evaluate)

	// Every user manages their own tokens.
	route("GET /api/v1/me", auth.RoleViewer, s.getMe)
	route("GET /api/v1/me/tokens", auth.RoleViewer, s.listMyTokens)
	secretRoute("POST /api/v1/me/tokens", auth.RoleViewer, s.createMyToken)
	route("DELETE /api/v1/me/tokens/{id}", auth.RoleViewer, s.revokeMyToken)

	route("GET /api/v1/users", auth.RoleAdmin, s.listUsers)
	route("POST /api/v1/users", auth.RoleAdmin, s.createUser)
	route("GET /api/v1/users/{handle}", auth.RoleAdmin, s.getUser)
	route("PUT /api/v1/users/{handle}/role", auth.RoleAdmin, s.setRole)
	route("DELETE /api/v1/users/{handle}", auth.RoleAdmin, s.disableUser)
	route("GET /api/v1/users/{handle}/audit", auth.RoleAdmin, s.listUserAudit)
	route("GET /api/v1/users/{handle}/tokens", auth.RoleAdmin, s.listUserTokens)
	secretRoute("POST /api/v1/users/{handle}/tokens", auth.RoleAdmin, s.createUserToken)
	route("DELETE /api/v1/users/{handle}/tokens/{id}", auth.RoleAdmin, s.revokeUserToken)

	route("GET /api/v1/sdk-keys", auth.RoleAdmin, s.listSDKKeys)
	secretRoute("POST /api/v1/sdk-keys", auth.RoleAdmin, s.createSDKKey)
	route("DELETE /api/v1/sdk-keys/{id}", auth.RoleAdmin, s.revokeSDKKey)

	route("POST /api/v1/environments", auth.RoleAdmin, s.createEnvironment)
	route("PUT /api/v1/environments/{key}", auth.RoleAdmin, s.updateEnvironmentSettings)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("/api/", s.authenticate(api))
	return mux
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// principal is who is calling: a user, or an app holding an SDK key.
type principal struct {
	user   *auth.User
	sdkEnv string
}

type principalKey struct{}

func principalFrom(r *http.Request) principal {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p
}

// actor is the audit log's name for the caller. Only user-only routes
// record changes, so the user is always set there.
func actor(r *http.Request) string { return principalFrom(r).user.Handle }

func (s *server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || secret == "" {
			unauthorized(w)
			return
		}
		hash := auth.HashSecret(secret)
		var p principal
		var err error
		if strings.HasPrefix(secret, auth.SDKKeyPrefix) {
			p.sdkEnv, err = s.users.AuthenticateSDKKey(r.Context(), hash)
		} else {
			var u auth.User
			u, err = s.users.Authenticate(r.Context(), hash)
			p.user = &u
		}
		if errors.Is(err, errs.ErrUnauthorized) {
			unauthorized(w)
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "missing or invalid credentials")
}

// requireRole allows users with at least min's permissions. SDK keys
// are always refused.
func requireRole(min auth.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := principalFrom(r).user
		if u == nil {
			writeError(w, http.StatusForbidden, "SDK keys can only call POST /api/v1/evaluate")
			return
		}
		if !u.Role.AtLeast(min) {
			writeError(w, http.StatusForbidden, "this needs the "+string(min)+" role or higher")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail maps store errors to responses. Unexpected errors are logged and
// hidden from the client.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, errs.ErrConflict):
		writeError(w, http.StatusConflict, "already exists")
	case errors.Is(err, errs.ErrInvalid):
		writeError(w, http.StatusBadRequest, validationMessage(err))
	default:
		s.log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// validationMessage drops the ErrInvalid sentinel from a joined error,
// leaving the human-readable reason.
func validationMessage(err error) string {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range j.Unwrap() {
			if e != errs.ErrInvalid {
				return e.Error()
			}
		}
	}
	return "invalid request"
}

// decode reads a single JSON object, rejecting unknown fields and
// oversized bodies. It writes the error response itself.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("body must contain a single JSON object")
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		}
		return false
	}
	return true
}
