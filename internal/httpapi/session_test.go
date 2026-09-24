package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
)

// login signs in with c's token and returns a client using the session.
func (c *client) login() *client {
	c.t.Helper()
	rec := c.post("/api/v1/session", `{"token":"`+c.token+`"}`, "localhost:8080", "same-origin")
	if rec.Code != http.StatusCreated {
		c.t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	cc := c.as("")
	cc.session = sessionFrom(c.t, rec)
	return cc
}

// post sends a browser-style request with no credentials.
func (c *client) post(path, body, host, fetchSite string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Host = host
	if fetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", fetchSite)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func sessionFrom(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == sessionCookie {
			return ck.Value
		}
	}
	t.Fatalf("no %s cookie in %v", sessionCookie, rec.Header()["Set-Cookie"])
	return ""
}

func TestSessionLoginAndLogout(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	rec := c.post("/api/v1/session", `{"token":"`+c.token+`"}`, "localhost:8080", "same-origin")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"handle":"mel"`) {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	ck := rec.Result().Cookies()[0]
	if ck.Name != sessionCookie || !strings.HasPrefix(ck.Value, auth.SessionPrefix) || !ck.HttpOnly ||
		ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" || ck.MaxAge != 12*60*60 || ck.Secure {
		t.Errorf("cookie = %+v", ck)
	}
	// The session secret is only in the cookie, never in the body.
	if strings.Contains(rec.Body.String(), ck.Value) || strings.Contains(rec.Body.String(), c.token) {
		t.Error("login response body contains a secret")
	}

	s := c.as("")
	s.session = ck.Value
	if out := s.mustDo("GET", "/api/v1/me", "", 200); out["handle"] != "mel" {
		t.Errorf("me = %v", out)
	}
	s.mustDo("POST", "/api/v1/flags", `{"key":"dark-mode","name":"Dark mode"}`, 201)
	events := c.mustDo("GET", "/api/v1/flags/dark-mode/audit", "", 200)["events"].([]any)
	if actor := events[0].(map[string]any)["actor"]; actor != "mel" {
		t.Errorf("change attributed to %v, want mel", actor)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/session", nil)
	req.Host = "localhost:8080"
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: ck.Value})
	out := httptest.NewRecorder()
	c.h.ServeHTTP(out, req)
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", out.Code)
	}
	if cleared := out.Result().Cookies()[0]; cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Errorf("logout cookie = %+v", cleared)
	}
	s.mustDo("GET", "/api/v1/me", "", 401)
}

func TestSessionCookieIsSecureExceptOnLocalhost(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	for host, secure := range map[string]bool{
		"localhost:3000": false, "127.0.0.1:8080": false, "[::1]:8080": false,
		"flags.example.com": true, "localhost.example.com": true,
	} {
		rec := c.post("/api/v1/session", `{"token":"`+c.token+`"}`, host, "same-origin")
		if ck := rec.Result().Cookies()[0]; ck.Secure != secure {
			t.Errorf("%s: Secure = %v, want %v", host, ck.Secure, secure)
		}
	}
	// Behind a TLS-terminating proxy.
	req := httptest.NewRequest("POST", "/api/v1/session", strings.NewReader(`{"token":"`+c.token+`"}`))
	req.Host = "localhost:8080"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if !rec.Result().Cookies()[0].Secure {
		t.Error("X-Forwarded-Proto https: cookie not Secure")
	}
}

func TestSessionLoginRejects(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	revoked := c.newUser("rev", auth.RoleAdmin)
	toks, _ := c.users.ListTokens(context.Background(), "rev")
	c.users.RevokeToken(context.Background(), "mel", "rev", toks[0].ID)
	session := c.login().session

	for name, body := range map[string]string{
		"wrong token":    `{"token":"fs_` + strings.Repeat("0", 64) + `"}`,
		"revoked token":  `{"token":"` + revoked + `"}`,
		"SDK key":        `{"token":"` + c.newSDKKey("prod") + `"}`,
		"session secret": `{"token":"` + session + `"}`,
		"empty":          `{"token":""}`,
	} {
		if rec := c.post("/api/v1/session", body, "localhost:8080", "same-origin"); rec.Code != 401 || len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s: got %d, cookies %v", name, rec.Code, rec.Result().Cookies())
		}
	}
	if rec := c.post("/api/v1/session", `{"token":"x","extra":1}`, "localhost:8080", "same-origin"); rec.Code != 400 {
		t.Errorf("unknown field: got %d", rec.Code)
	}
}

func TestSessionRefusesCrossSiteRequests(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	c.newUser("sam", auth.RoleEditor)
	c.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout"}`, 201)
	c.mustDo("POST", "/api/v1/flags", `{"key":"other","name":"Other"}`, 201)
	s := c.login()

	// A page on another site can't log someone in...
	if rec := c.post("/api/v1/session", `{"token":"`+c.token+`"}`, "localhost:8080", "cross-site"); rec.Code != 403 {
		t.Errorf("cross-site login: got %d", rec.Code)
	}
	// ...or make changes with their session cookie.
	for name, set := range map[string]func(*http.Request){
		"Sec-Fetch-Site": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"Origin":         func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
	} {
		for _, method := range []string{"POST", "PUT", "DELETE"} {
			before := snapshot(t, c)
			req := httptest.NewRequest(method, "/api/v1/flags", strings.NewReader(`{"key":"evil","name":"Evil"}`))
			req.Host = "localhost:8080"
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.session})
			set(req)
			rec := httptest.NewRecorder()
			c.h.ServeHTTP(rec, req)
			if rec.Code != 403 || snapshot(t, c) != before {
				t.Errorf("%s %s: got %d", name, method, rec.Code)
			}
		}
		req := httptest.NewRequest("DELETE", "/api/v1/session", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.session})
		set(req)
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Errorf("%s logout: got %d", name, rec.Code)
		}
	}
	// The session still works from the dashboard itself.
	s.mustDo("GET", "/api/v1/me", "", 200)
}

func TestSessionEndsWithItsToken(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	s := c.as(c.newUser("sam", auth.RoleEditor)).login()
	s.mustDo("GET", "/api/v1/me", "", 200)
	toks := s.mustDo("GET", "/api/v1/me/tokens", "", 200)["tokens"].([]any)
	id := strconv.FormatFloat(toks[0].(map[string]any)["id"].(float64), 'f', 0, 64)
	s.mustDo("DELETE", "/api/v1/me/tokens/"+id, "", 204)
	s.mustDo("GET", "/api/v1/me", "", 401)

	// Disabling a user ends their sessions too.
	k := c.as(c.newUser("kim", auth.RoleEditor)).login()
	c.mustDo("DELETE", "/api/v1/users/kim", "", 204)
	k.mustDo("GET", "/api/v1/me", "", 401)
}

func TestBearerTakesPrecedenceOverSession(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	s := c.login()
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.session})
	req.Header.Set("Authorization", "Bearer fs_wrong")
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("bad bearer with good cookie: got %d, want 401", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	for _, path := range []string{"/healthz", "/api/v1/me", "/api/v1/nope"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+c.token)
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		h := rec.Header()
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" ||
			h.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Errorf("%s: headers = %v", path, h)
		}
		if api := strings.HasPrefix(path, "/api/"); (h.Get("Cache-Control") == "no-store") != api {
			t.Errorf("%s: Cache-Control = %q", path, h.Get("Cache-Control"))
		}
	}
}
