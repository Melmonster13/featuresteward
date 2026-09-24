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
)

const maxBodyBytes = 1 << 20

type server struct {
	flags flag.Store
	users auth.Store
	log   *slog.Logger
}

// NewRouter returns the API handler. Every /api/v1 route needs
// "Authorization: Bearer <credential>": a user's API token, or for
// POST /api/v1/evaluate only, an SDK key.
func NewRouter(flags flag.Store, users auth.Store, log *slog.Logger) http.Handler {
	s := &server{flags: flags, users: users, log: log}

	api := http.NewServeMux()
	user := func(pattern string, h http.HandlerFunc) { api.Handle(pattern, userOnly(h)) }
	user("GET /api/v1/environments", s.listEnvironments)
	user("GET /api/v1/flags", s.listFlags)
	user("POST /api/v1/flags", s.createFlag)
	user("GET /api/v1/flags/{key}", s.getFlag)
	user("PUT /api/v1/flags/{key}", s.updateFlag)
	user("DELETE /api/v1/flags/{key}", s.archiveFlag)
	user("PUT /api/v1/flags/{key}/environments/{env}", s.updateEnvironment)
	user("GET /api/v1/flags/{key}/audit", s.listAudit)
	api.HandleFunc("POST /api/v1/evaluate", s.evaluate)

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

func userOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principalFrom(r).user == nil {
			writeError(w, http.StatusForbidden, "SDK keys can only call POST /api/v1/evaluate")
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
