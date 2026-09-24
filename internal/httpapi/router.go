// Package httpapi serves the FeatureSteward REST API.
package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Melmonster13/featuresteward/internal/flag"
)

// actor is recorded in the audit log until per-user auth exists.
const actor = "api-key"

const maxBodyBytes = 1 << 20

type server struct {
	store  flag.Store
	apiKey []byte
	log    *slog.Logger
}

// NewRouter returns the API handler. Every /api/v1 route requires
// "Authorization: Bearer <apiKey>".
func NewRouter(store flag.Store, apiKey string, log *slog.Logger) http.Handler {
	s := &server{store: store, apiKey: []byte(apiKey), log: log}

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/environments", s.listEnvironments)
	api.HandleFunc("GET /api/v1/flags", s.listFlags)
	api.HandleFunc("POST /api/v1/flags", s.createFlag)
	api.HandleFunc("GET /api/v1/flags/{key}", s.getFlag)
	api.HandleFunc("PUT /api/v1/flags/{key}", s.updateFlag)
	api.HandleFunc("DELETE /api/v1/flags/{key}", s.archiveFlag)
	api.HandleFunc("PUT /api/v1/flags/{key}/environments/{env}", s.updateEnvironment)
	api.HandleFunc("GET /api/v1/flags/{key}/audit", s.listAudit)
	api.HandleFunc("POST /api/v1/evaluate", s.evaluate)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("/api/", s.requireAPIKey(api))
	return mux
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func (s *server) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(token), s.apiKey) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing or invalid API key")
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
	case errors.Is(err, flag.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, flag.ErrConflict):
		writeError(w, http.StatusConflict, "a flag with this key already exists")
	case errors.Is(err, flag.ErrInvalid):
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
			if e != flag.ErrInvalid {
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
