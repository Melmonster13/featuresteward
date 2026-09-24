package httpapi

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/errs"
)

// sessionCookie holds a browser session. It is HttpOnly so page scripts
// can't read it, and SameSite=Strict so other sites can't send it.
const sessionCookie = "fs_session"

// crossOrigin rejects state-changing browser requests from other sites.
// Browsers send session cookies automatically, so cookie-authenticated
// changes must come from the dashboard's own pages.
var crossOrigin = http.NewCrossOriginProtection()

// createSession exchanges an API token for a session cookie.
func (s *server) createSession(w http.ResponseWriter, r *http.Request) {
	if err := crossOrigin.Check(r); err != nil {
		writeError(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !strings.HasPrefix(req.Token, auth.TokenPrefix) ||
		strings.HasPrefix(req.Token, auth.SDKKeyPrefix) || strings.HasPrefix(req.Token, auth.SessionPrefix) {
		writeError(w, http.StatusUnauthorized, "sign in with an API token (fs_…), not an SDK key")
		return
	}
	secret, hash, _ := auth.NewSecret(auth.SessionPrefix)
	expires := time.Now().Add(auth.SessionTTL)
	u, err := s.users.CreateSession(r.Context(), auth.HashSecret(req.Token), hash, expires)
	if errors.Is(err, errs.ErrUnauthorized) {
		writeError(w, http.StatusUnauthorized, "invalid, expired, or revoked token")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, newSessionCookie(r, secret, int(auth.SessionTTL.Seconds())))
	writeJSON(w, http.StatusCreated, toUserJSON(u))
}

// deleteSession logs out: it ends the session and clears the cookie.
func (s *server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := crossOrigin.Check(r); err != nil {
		writeError(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.users.DeleteSession(r.Context(), auth.HashSecret(c.Value)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	http.SetCookie(w, newSessionCookie(r, "", -1))
	w.WriteHeader(http.StatusNoContent)
}

func newSessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secureCookie(r),
		SameSite: http.SameSiteStrictMode,
	}
}

// secureCookie is false only for plain http to this machine, so the
// dashboard works at http://localhost during development. Elsewhere
// browsers send the cookie only over https.
func secureCookie(r *http.Request) bool {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	switch strings.Trim(host, "[]") {
	case "localhost", "127.0.0.1", "::1":
		return false
	}
	return true
}

// securityHeaders limits what a browser will do with any response. The
// dashboard's pages set their own Content-Security-Policy.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
