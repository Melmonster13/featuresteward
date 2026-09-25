package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/auth/authtest"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
	"github.com/Melmonster13/featuresteward/internal/idempotency/idemtest"
)

type client struct {
	t     *testing.T
	h     http.Handler
	users *authtest.Memory
	idem  *idemtest.Memory
	token string // sent as the bearer credential
	// session, if set, is sent as the session cookie instead, with the
	// headers a browser adds for a same-origin request.
	session string
}

// newClient returns a client authenticated as admin "mel".
func newClient(t *testing.T, flags flag.Store) *client {
	users, idem := authtest.NewMemory(), idemtest.NewMemory()
	c := &client{t: t, users: users, idem: idem,
		h: NewRouter(flags, users, idem, slog.New(slog.NewTextHandler(io.Discard, nil)))}
	c.token = c.newUser("mel", auth.RoleAdmin)
	return c
}

// newUser creates a user and returns a token for them.
func (c *client) newUser(handle string, role auth.Role) string {
	c.t.Helper()
	if _, err := c.users.CreateUser(context.Background(), "system", handle, "", role); err != nil {
		c.t.Fatal(err)
	}
	secret, hash, prefix := auth.NewSecret(auth.TokenPrefix)
	if _, err := c.users.CreateToken(context.Background(), "system", handle, "test", hash, prefix, nil); err != nil {
		c.t.Fatal(err)
	}
	return secret
}

func (c *client) newSDKKey(env string) string {
	c.t.Helper()
	secret, hash, prefix := auth.NewSecret(auth.SDKKeyPrefix)
	if _, err := c.users.CreateSDKKey(context.Background(), "mel", env, "test", hash, prefix); err != nil {
		c.t.Fatal(err)
	}
	return secret
}

// as returns a client sending a different credential.
func (c *client) as(token string) *client {
	cc := *c
	cc.token = token
	return &cc
}

// do sends a request and decodes a JSON response into a map.
func (c *client) do(method, path, body string) (int, map[string]any) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if c.session != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.session})
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	} else if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			c.t.Fatalf("%s %s: non-JSON body %q", method, path, rec.Body.String())
		}
	}
	return rec.Code, out
}

func (c *client) mustDo(method, path, body string, want int) map[string]any {
	c.t.Helper()
	code, out := c.do(method, path, body)
	if code != want {
		c.t.Fatalf("%s %s = %d %v, want %d", method, path, code, out, want)
	}
	return out
}

func TestHealthzNeedsNoAuth(t *testing.T) {
	rec := httptest.NewRecorder()
	newClient(t, flagtest.NewMemory()).h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestHealthzReportsOptionalDependencies(t *testing.T) {
	h := NewRouter(flagtest.NewMemory(), authtest.NewMemory(), idemtest.NewMemory(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithHealthCheck("redis", func(context.Context) error { return errors.New("connection refused") }),
		WithHealthCheck("cache", func(context.Context) error { return nil }),
		WithHealthCheck("extra", nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var got map[string]string
	json.Unmarshal(rec.Body.Bytes(), &got)
	want := map[string]string{"status": "ok", "redis": "unavailable", "cache": "ok", "extra": "disabled"}
	if rec.Code != http.StatusOK || len(got) != len(want) {
		t.Fatalf("got %d %v", rec.Code, got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// The error's text isn't exposed to unauthenticated callers.
	if strings.Contains(rec.Body.String(), "refused") {
		t.Error("healthz leaked an error message")
	}
}

func TestAuth(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	c.mustDo("GET", "/api/v1/flags", "", 200)

	revoked := c.newUser("rev", auth.RoleAdmin)
	toks, _ := c.users.ListTokens(context.Background(), "rev")
	c.users.RevokeToken(context.Background(), "mel", "rev", toks[0].ID)
	disabled := c.newUser("gone", auth.RoleAdmin)
	c.users.DisableUser(context.Background(), "mel", "gone")

	for name, header := range map[string]string{
		"none":          "",
		"empty bearer":  "Bearer ",
		"wrong token":   "Bearer fs_" + strings.Repeat("0", 64),
		"wrong SDK key": "Bearer " + auth.SDKKeyPrefix + strings.Repeat("0", 64),
		"not bearer":    "Basic " + c.token,
		"no scheme":     c.token,
		"extra char":    "Bearer " + c.token + "x",
		"revoked token": "Bearer " + revoked,
		"disabled user": "Bearer " + disabled,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: got %d, want 401 with WWW-Authenticate", name, rec.Code)
		}
	}
	// Unknown API paths also require auth, so they don't reveal what exists.
	if code, _ := c.as("").do("GET", "/api/v1/nope", ""); code != http.StatusUnauthorized {
		t.Errorf("unknown path: got %d, want 401", code)
	}
}

func TestSDKKeys(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	c.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout"}`, 201)
	c.mustDo("PUT", "/api/v1/flags/new-checkout/environments/prod", `{"enabled":true,"rollout_percentage":100,"reason":"test"}`, 200)
	sdk := c.as(c.newSDKKey("prod"))

	got := sdk.mustDo("POST", "/api/v1/evaluate", `{"flag":"new-checkout","environment":"prod","user_id":"u"}`, 200)
	if got["enabled"] != true {
		t.Errorf("evaluate = %v", got)
	}
	// The key's environment is the default.
	got = sdk.mustDo("POST", "/api/v1/evaluate", `{"flag":"new-checkout","user_id":"u"}`, 200)
	if got["enabled"] != true {
		t.Errorf("evaluate without environment = %v", got)
	}
	if code, out := sdk.do("POST", "/api/v1/evaluate", `{"flag":"new-checkout","environment":"dev"}`); code != 403 ||
		!strings.Contains(out["error"].(string), "environment prod") {
		t.Errorf("other environment: got %d %v", code, out)
	}
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/v1/flags", ""},
		{"GET", "/api/v1/flags/new-checkout", ""},
		{"GET", "/api/v1/environments", ""},
		{"POST", "/api/v1/flags", `{"key":"x","name":"x"}`},
		{"PUT", "/api/v1/flags/new-checkout/environments/prod", `{"enabled":false,"rollout_percentage":0}`},
		{"DELETE", "/api/v1/flags/new-checkout", ""},
		{"GET", "/api/v1/flags/new-checkout/audit", ""},
	} {
		if code, _ := sdk.do(r.method, r.path, r.body); code != 403 {
			t.Errorf("SDK key %s %s: got %d, want 403", r.method, r.path, code)
		}
	}

	// A revoked key stops working.
	keys, _ := c.users.ListSDKKeys(context.Background())
	c.users.RevokeSDKKey(context.Background(), "mel", keys[0].ID)
	if code, _ := sdk.do("POST", "/api/v1/evaluate", `{"flag":"new-checkout"}`); code != 401 {
		t.Errorf("revoked key: got %d, want 401", code)
	}
}

func TestChangesAreAttributedToTheCaller(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	sam := c.as(c.newUser("sam", auth.RoleAdmin))
	sam.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout"}`, 201)
	c.mustDo("PUT", "/api/v1/flags/new-checkout/environments/dev", `{"enabled":true,"rollout_percentage":100}`, 200)

	events := c.mustDo("GET", "/api/v1/flags/new-checkout/audit", "", 200)["events"].([]any)
	if len(events) != 2 || events[0].(map[string]any)["actor"] != "sam" || events[1].(map[string]any)["actor"] != "mel" {
		t.Fatalf("events = %v", events)
	}
}

func TestFlagLifecycle(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())

	f := c.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout","description":"v2 flow"}`, 201)
	if f["key"] != "new-checkout" || f["archived_at"] != nil {
		t.Fatalf("created = %v", f)
	}
	prod := f["environments"].(map[string]any)["prod"].(map[string]any)
	if prod["enabled"] != false || prod["rollout_percentage"] != 100.0 {
		t.Fatalf("new prod config = %v", prod)
	}

	c.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"Again"}`, 409)
	c.mustDo("GET", "/api/v1/flags/new-checkout", "", 200)

	f = c.mustDo("PUT", "/api/v1/flags/new-checkout", `{"name":"Checkout v2","description":""}`, 200)
	if f["name"] != "Checkout v2" {
		t.Fatalf("renamed = %v", f)
	}

	f = c.mustDo("PUT", "/api/v1/flags/new-checkout/environments/prod",
		`{"enabled":true,"rollout_percentage":25,"rules":[{"attribute":"group","values":["staff"],"serve":true}],"reason":"test"}`, 200)
	prod = f["environments"].(map[string]any)["prod"].(map[string]any)
	if prod["enabled"] != true || prod["rollout_percentage"] != 25.0 || len(prod["rules"].([]any)) != 1 {
		t.Fatalf("prod after update = %v", prod)
	}

	list := c.mustDo("GET", "/api/v1/flags", "", 200)
	if n := len(list["flags"].([]any)); n != 1 {
		t.Fatalf("listed %d flags", n)
	}

	c.mustDo("DELETE", "/api/v1/flags/new-checkout", "", 204)
	c.mustDo("DELETE", "/api/v1/flags/new-checkout", "", 404)
	list = c.mustDo("GET", "/api/v1/flags", "", 200)
	if n := len(list["flags"].([]any)); n != 0 {
		t.Fatalf("archived flag still listed")
	}
	// Archived flags stay readable, with their history.
	f = c.mustDo("GET", "/api/v1/flags/new-checkout", "", 200)
	if f["archived_at"] == nil {
		t.Fatal("archived_at not set")
	}

	audit := c.mustDo("GET", "/api/v1/flags/new-checkout/audit", "", 200)
	var actions []string
	for _, e := range audit["events"].([]any) {
		ev := e.(map[string]any)
		actions = append(actions, ev["action"].(string))
		if ev["actor"] != "mel" {
			t.Errorf("actor = %v", ev["actor"])
		}
	}
	want := []string{flag.ActionCreated, flag.ActionUpdated, flag.ActionEnvUpdated, flag.ActionArchived}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Errorf("audit actions = %v, want %v", actions, want)
	}
	env := audit["events"].([]any)[2].(map[string]any)
	if env["environment"] != "prod" || env["before"].(map[string]any)["enabled"] != false || env["after"].(map[string]any)["rollout_percentage"] != 25.0 {
		t.Errorf("env audit event = %v", env)
	}
}

func TestEvaluate(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	c.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout"}`, 201)
	c.mustDo("PUT", "/api/v1/flags/new-checkout/environments/prod",
		`{"enabled":true,"rollout_percentage":25,"rules":[{"attribute":"group","values":["staff"],"serve":true}],"reason":"test"}`, 200)

	tests := []struct {
		body        string
		wantEnabled bool
		wantReason  string
	}{
		// user-42 is in bucket 14 for new-checkout (see eval tests).
		{`{"flag":"new-checkout","environment":"prod","user_id":"user-42"}`, true, "percentage_rollout"},
		{`{"flag":"new-checkout","environment":"prod","user_id":"user-1"}`, false, "percentage_rollout"},
		{`{"flag":"new-checkout","environment":"prod","user_id":"user-1","groups":["staff"]}`, true, "rule_match"},
		{`{"flag":"new-checkout","environment":"prod"}`, false, "missing_user_id"},
		{`{"flag":"new-checkout","environment":"dev","user_id":"user-42"}`, false, "disabled"},
	}
	for _, tt := range tests {
		got := c.mustDo("POST", "/api/v1/evaluate", tt.body, 200)
		if got["flag"] != "new-checkout" || got["enabled"] != tt.wantEnabled || got["reason"] != tt.wantReason {
			t.Errorf("%s = %v, want enabled=%v reason=%s", tt.body, got, tt.wantEnabled, tt.wantReason)
		}
	}

	c.mustDo("POST", "/api/v1/evaluate", `{"flag":"nope","environment":"prod"}`, 404)
	c.mustDo("POST", "/api/v1/evaluate", `{"flag":"new-checkout","environment":"qa"}`, 404)
	c.mustDo("POST", "/api/v1/evaluate", `{"flag":"new-checkout"}`, 400)
}

func TestBadRequests(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	c.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout"}`, 201)

	tests := []struct {
		method, path, body string
		want               int
		wantErr            string
	}{
		{"POST", "/api/v1/flags", `{"key":"Bad Key","name":"x"}`, 400, "key must be"},
		{"POST", "/api/v1/flags", `{"key":"ok","name":""}`, 400, "name is required"},
		{"POST", "/api/v1/flags", `{"key":"ok","name":"x","owner":"mel"}`, 400, "unknown field"},
		{"POST", "/api/v1/flags", `{"key":"ok","name":"x"}{}`, 400, "single JSON object"},
		{"POST", "/api/v1/flags", `not json`, 400, "invalid JSON"},
		{"POST", "/api/v1/flags", `{"key":"ok","name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, 413, "too large"},
		{"PUT", "/api/v1/flags/new-checkout/environments/prod", `{"enabled":true}`, 400, "required"},
		{"PUT", "/api/v1/flags/new-checkout/environments/prod", `{"enabled":true,"rollout_percentage":101}`, 400, "0-100"},
		{"PUT", "/api/v1/flags/new-checkout/environments/prod",
			`{"enabled":true,"rollout_percentage":1,"rules":[{"attribute":"country","values":["NZ"]}]}`, 400, "rule attribute"},
		{"PUT", "/api/v1/flags/new-checkout/environments/qa", `{"enabled":true,"rollout_percentage":1}`, 404, "not found"},
		{"GET", "/api/v1/flags/nope", "", 404, "not found"},
		{"GET", "/api/v1/flags/nope/audit", "", 404, "not found"},
	}
	for _, tt := range tests {
		code, out := c.do(tt.method, tt.path, tt.body)
		msg, _ := out["error"].(string)
		if code != tt.want || !strings.Contains(msg, tt.wantErr) {
			name := tt.body
			if len(name) > 80 {
				name = name[:80] + "..."
			}
			t.Errorf("%s %s %s = %d %q, want %d containing %q", tt.method, tt.path, name, code, msg, tt.want, tt.wantErr)
		}
	}
}

type brokenStore struct{ *flagtest.Memory }

func (brokenStore) ListFlags(context.Context) ([]flag.Flag, error) {
	return nil, errors.New("connection refused: postgres://secret@db")
}

func TestInternalErrorsAreHidden(t *testing.T) {
	c := newClient(t, brokenStore{flagtest.NewMemory()})
	code, out := c.do("GET", "/api/v1/flags", "")
	if code != 500 || out["error"] != "internal error" {
		t.Fatalf("got %d %v", code, out)
	}
}

// TestRolePermissions checks every route against every role, and that a
// denied request changes nothing. Add new routes here.
func TestRolePermissions(t *testing.T) {
	routes := []struct {
		method, path, body string
		min                auth.Role
		want               int // status when allowed
	}{
		{"GET", "/api/v1/environments", "", auth.RoleViewer, 200},
		{"GET", "/api/v1/flags", "", auth.RoleViewer, 200},
		{"GET", "/api/v1/flags/new-checkout", "", auth.RoleViewer, 200},
		{"GET", "/api/v1/flags/new-checkout/audit", "", auth.RoleViewer, 200},
		{"POST", "/api/v1/evaluate", `{"flag":"new-checkout","environment":"prod"}`, auth.RoleViewer, 200},
		{"GET", "/api/v1/me", "", auth.RoleViewer, 200},
		{"GET", "/api/v1/me/tokens", "", auth.RoleViewer, 200},
		{"POST", "/api/v1/me/tokens", `{"name":"cli"}`, auth.RoleViewer, 201},
		{"DELETE", "/api/v1/me/tokens/{myToken}", "", auth.RoleViewer, 204},

		{"POST", "/api/v1/flags", `{"key":"another","name":"Another"}`, auth.RoleEditor, 201},
		{"PUT", "/api/v1/flags/new-checkout", `{"name":"Renamed"}`, auth.RoleEditor, 200},
		{"PUT", "/api/v1/flags/new-checkout/environments/dev", `{"enabled":true,"rollout_percentage":50}`, auth.RoleEditor, 200},
		{"PUT", "/api/v1/flags/new-checkout/environments/staging", `{"enabled":true,"rollout_percentage":50}`, auth.RoleEditor, 200},

		// Admins with a reason (emergency); everyone else needs a change request.
		{"PUT", "/api/v1/flags/new-checkout/environments/prod", `{"enabled":true,"rollout_percentage":50,"reason":"outage"}`, auth.RoleAdmin, 200},
		// The kill switch: turning prod off directly.
		{"PUT", "/api/v1/flags/new-checkout/environments/prod", `{"enabled":false,"rollout_percentage":100}`, auth.RoleEditor, 200},
		{"POST", "/api/v1/flags/new-checkout/environments/prod/requests", `{"enabled":true,"rollout_percentage":50,"reason":"launch"}`, auth.RoleEditor, 201},
		{"GET", "/api/v1/requests", "", auth.RoleViewer, 200},
		{"GET", "/api/v1/requests/{request}", "", auth.RoleViewer, 200},
		// other's steward is mel, so only approvers and admins review it here.
		{"POST", "/api/v1/requests/{request}/approve", `{"comment":"ok"}`, auth.RoleApprover, 200},
		{"POST", "/api/v1/requests/{request}/reject", `{"comment":"no"}`, auth.RoleApprover, 200},
		{"DELETE", "/api/v1/flags/new-checkout", "", auth.RoleAdmin, 204},
		// new-checkout's steward is mel, so only admins can reassign it here.
		{"PUT", "/api/v1/flags/new-checkout/steward", `{"steward":"sam"}`, auth.RoleAdmin, 200},
		// Likewise for marking it permanent.
		{"PUT", "/api/v1/flags/new-checkout/permanent", `{"reason":"ops kill switch"}`, auth.RoleAdmin, 200},
		{"GET", "/api/v1/users", "", auth.RoleAdmin, 200},
		{"POST", "/api/v1/users", `{"handle":"new","role":"viewer"}`, auth.RoleAdmin, 201},
		{"GET", "/api/v1/users/sam", "", auth.RoleAdmin, 200},
		{"PUT", "/api/v1/users/sam/role", `{"role":"viewer"}`, auth.RoleAdmin, 200},
		{"DELETE", "/api/v1/users/sam", "", auth.RoleAdmin, 204},
		{"GET", "/api/v1/users/sam/audit", "", auth.RoleAdmin, 200},
		{"GET", "/api/v1/users/sam/tokens", "", auth.RoleAdmin, 200},
		{"POST", "/api/v1/users/sam/tokens", `{"name":"onboarding"}`, auth.RoleAdmin, 201},
		{"DELETE", "/api/v1/users/sam/tokens/{samToken}", "", auth.RoleAdmin, 204},
		{"GET", "/api/v1/sdk-keys", "", auth.RoleAdmin, 200},
		{"POST", "/api/v1/sdk-keys", `{"environment":"prod","name":"svc"}`, auth.RoleAdmin, 201},
		{"DELETE", "/api/v1/sdk-keys/{sdkKey}", "", auth.RoleAdmin, 204},
		{"POST", "/api/v1/environments", `{"key":"qa","name":"QA","protected":false}`, auth.RoleAdmin, 201},
		{"PUT", "/api/v1/environments/staging", `{"name":"Staging","protected":true}`, auth.RoleAdmin, 200},
	}
	ctx := context.Background()
	for _, via := range []string{"token", "session"} {
		for _, role := range []auth.Role{auth.RoleViewer, auth.RoleEditor, auth.RoleApprover, auth.RoleAdmin} {
			for _, rt := range routes {
				t.Run(via+" "+string(role)+" "+rt.method+" "+rt.path, func(t *testing.T) {
					c := newClient(t, flagtest.NewMemory())
					c.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout"}`, 201)
					sam := c.as(c.newUser("sam", auth.RoleEditor))
					c.newSDKKey("prod")
					// A pending request by sam for u to review.
					c.mustDo("POST", "/api/v1/flags", `{"key":"other","name":"Other"}`, 201)
					req := sam.mustDo("POST", "/api/v1/flags/other/environments/prod/requests", `{"enabled":true,"rollout_percentage":10}`, 201)
					u := c.as(c.newUser("u", role))
					if via == "session" {
						u = u.login()
					}

					samToks, _ := c.users.ListTokens(ctx, "sam")
					myToks, _ := c.users.ListTokens(ctx, "u")
					keys, _ := c.users.ListSDKKeys(ctx)
					path := strings.NewReplacer(
						"{samToken}", strconv.FormatInt(samToks[0].ID, 10),
						"{myToken}", strconv.FormatInt(myToks[0].ID, 10),
						"{sdkKey}", strconv.FormatInt(keys[0].ID, 10),
						"{request}", strconv.FormatFloat(req["id"].(float64), 'f', 0, 64),
					).Replace(rt.path)

					before := snapshot(t, c)
					code, out := u.do(rt.method, path, rt.body)
					want := rt.want
					if !role.AtLeast(rt.min) {
						want = 403
					}
					if code != want {
						t.Fatalf("got %d %v, want %d", code, out, want)
					}
					if code == 403 && snapshot(t, c) != before {
						t.Errorf("denied request changed state")
					}
				})
			}
		}
	}
}

// snapshot captures everything the routes can change, as seen by admin mel.
func snapshot(t *testing.T, c *client) string {
	t.Helper()
	var b strings.Builder
	for _, path := range []string{
		"/api/v1/flags/new-checkout", "/api/v1/flags/new-checkout/audit", "/api/v1/flags",
		"/api/v1/users", "/api/v1/users/sam/audit", "/api/v1/users/sam/tokens",
		"/api/v1/sdk-keys", "/api/v1/environments", "/api/v1/requests", "/api/v1/flags/other",
	} {
		out, _ := json.Marshal(c.mustDo("GET", path, "", 200))
		b.Write(out)
	}
	return b.String()
}

func TestEnvironmentsShowProtection(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	envs := c.mustDo("GET", "/api/v1/environments", "", 200)["environments"].([]any)
	prot := map[string]bool{}
	for _, e := range envs {
		m := e.(map[string]any)
		prot[m["key"].(string)] = m["protected"].(bool)
	}
	if len(prot) != 3 || !prot["prod"] || prot["dev"] || prot["staging"] {
		t.Fatalf("environments = %v", envs)
	}
}

func TestOnboardingAndTokens(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	c.mustDo("POST", "/api/v1/users", `{"handle":"sam","name":"Sam","role":"editor"}`, 201)

	// An admin issues sam's first token; it's returned once, uncached.
	req := httptest.NewRequest("POST", "/api/v1/users/sam/tokens", strings.NewReader(`{"name":"onboarding"}`))
	req.Header.Set("Authorization", "Bearer "+c.token)
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	secret, _ := created["token"].(string)
	if rec.Code != 201 || !strings.HasPrefix(secret, auth.TokenPrefix) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create token = %d %v, Cache-Control %q", rec.Code, created, rec.Header().Get("Cache-Control"))
	}

	sam := c.as(secret)
	me := sam.mustDo("GET", "/api/v1/me", "", 200)
	if me["handle"] != "sam" || me["role"] != "editor" {
		t.Fatalf("me = %v", me)
	}
	// Sam makes a personal token, then revokes the onboarding one.
	mine := sam.mustDo("POST", "/api/v1/me/tokens", `{"name":"laptop"}`, 201)
	sam2 := c.as(mine["token"].(string))
	toks := sam2.mustDo("GET", "/api/v1/me/tokens", "", 200)["tokens"].([]any)
	if len(toks) != 2 {
		t.Fatalf("tokens = %v", toks)
	}
	for _, tk := range toks {
		if _, leaked := tk.(map[string]any)["token"]; leaked {
			t.Errorf("listed token includes its secret: %v", tk)
		}
	}
	sam2.mustDo("DELETE", "/api/v1/me/tokens/"+strconv.FormatInt(int64(created["id"].(float64)), 10), "", 204)
	if code, _ := sam.do("GET", "/api/v1/me", ""); code != 401 {
		t.Errorf("revoked onboarding token: got %d", code)
	}
	sam2.mustDo("GET", "/api/v1/me", "", 200)

	// Users can't revoke someone else's token through /me.
	melToks := c.mustDo("GET", "/api/v1/me/tokens", "", 200)["tokens"].([]any)
	melID := strconv.FormatInt(int64(melToks[0].(map[string]any)["id"].(float64)), 10)
	sam2.mustDo("DELETE", "/api/v1/me/tokens/"+melID, "", 404)
	c.mustDo("GET", "/api/v1/me", "", 200)

	// The audit trail shows who did what to sam's account.
	events := c.mustDo("GET", "/api/v1/users/sam/audit", "", 200)["events"].([]any)
	var got []string
	for _, e := range events {
		m := e.(map[string]any)
		got = append(got, m["actor"].(string)+":"+m["action"].(string))
	}
	want := "mel:user.created,mel:token.created,sam:token.created,sam:token.revoked"
	if strings.Join(got, ",") != want {
		t.Errorf("audit = %v, want %s", got, want)
	}
}

func TestAdminsCantLockThemselvesOut(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	c.mustDo("PUT", "/api/v1/users/mel/role", `{"role":"viewer"}`, 403)
	c.mustDo("DELETE", "/api/v1/users/mel", "", 403)
	if me := c.mustDo("GET", "/api/v1/me", "", 200); me["role"] != "admin" {
		t.Fatalf("mel = %v", me)
	}
	// Another admin can.
	ana := c.as(c.newUser("ana", auth.RoleAdmin))
	ana.mustDo("PUT", "/api/v1/users/mel/role", `{"role":"editor"}`, 200)
	if me := c.mustDo("GET", "/api/v1/me", "", 200); me["role"] != "editor" {
		t.Fatalf("mel after demotion = %v", me)
	}
	ana.mustDo("DELETE", "/api/v1/users/mel", "", 204)
	if code, _ := c.do("GET", "/api/v1/me", ""); code != 401 {
		t.Errorf("disabled user's token: got %d", code)
	}
	u := ana.mustDo("GET", "/api/v1/users/mel", "", 200)
	if u["disabled_at"] == nil {
		t.Errorf("mel not marked disabled: %v", u)
	}
}

func TestSDKKeyEndpoints(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	c.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout"}`, 201)
	c.mustDo("PUT", "/api/v1/flags/new-checkout/environments/staging", `{"enabled":true,"rollout_percentage":100}`, 200)

	k := c.mustDo("POST", "/api/v1/sdk-keys", `{"environment":"staging","name":"checkout-service"}`, 201)
	secret := k["key"].(string)
	if !strings.HasPrefix(secret, auth.SDKKeyPrefix) || k["environment"] != "staging" {
		t.Fatalf("created = %v", k)
	}
	app := c.as(secret)
	if got := app.mustDo("POST", "/api/v1/evaluate", `{"flag":"new-checkout","user_id":"u"}`, 200); got["enabled"] != true {
		t.Errorf("evaluate = %v", got)
	}
	listed := c.mustDo("GET", "/api/v1/sdk-keys", "", 200)["sdk_keys"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["key"] != nil {
		t.Errorf("listed = %v", listed)
	}
	c.mustDo("DELETE", "/api/v1/sdk-keys/"+strconv.FormatInt(int64(k["id"].(float64)), 10), "", 204)
	if code, _ := app.do("POST", "/api/v1/evaluate", `{"flag":"new-checkout"}`); code != 401 {
		t.Errorf("revoked key: got %d", code)
	}
	c.mustDo("POST", "/api/v1/sdk-keys", `{"environment":"qa","name":"x"}`, 404)
	c.mustDo("POST", "/api/v1/sdk-keys", `{"environment":"prod","name":""}`, 400)
	c.mustDo("DELETE", "/api/v1/sdk-keys/999", "", 404)
	c.mustDo("DELETE", "/api/v1/sdk-keys/abc", "", 404)
}

func TestEnvironmentEndpoints(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	c.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout"}`, 201)
	editor := c.as(c.newUser("sam", auth.RoleEditor))

	env := c.mustDo("POST", "/api/v1/environments", `{"key":"qa","name":"QA","protected":false}`, 201)
	if env["key"] != "qa" || env["protected"] != false {
		t.Fatalf("created = %v", env)
	}
	// Existing flags get the new environment, and editors can change it.
	f := c.mustDo("GET", "/api/v1/flags/new-checkout", "", 200)
	if _, ok := f["environments"].(map[string]any)["qa"]; !ok {
		t.Fatalf("flag lacks qa: %v", f)
	}
	editor.mustDo("PUT", "/api/v1/flags/new-checkout/environments/qa", `{"enabled":true,"rollout_percentage":100}`, 200)

	// Once it's protected, editors need a change request to turn it on.
	c.mustDo("PUT", "/api/v1/environments/qa", `{"name":"QA","protected":true}`, 200)
	editor.mustDo("PUT", "/api/v1/flags/new-checkout/environments/qa", `{"enabled":true,"rollout_percentage":50}`, 403)

	c.mustDo("POST", "/api/v1/environments", `{"key":"qa","name":"Again"}`, 409)
	c.mustDo("POST", "/api/v1/environments", `{"key":"Bad Key","name":"x"}`, 400)
	c.mustDo("PUT", "/api/v1/environments/qa", `{"name":"QA"}`, 400)
	c.mustDo("PUT", "/api/v1/environments/nope", `{"name":"x","protected":false}`, 404)
}

func TestAdminBadRequests(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	c.mustDo("POST", "/api/v1/users", `{"handle":"sam","role":"owner"}`, 400)
	c.mustDo("POST", "/api/v1/users", `{"handle":"Sam","role":"viewer"}`, 400)
	c.mustDo("POST", "/api/v1/users", `{"handle":"mel","role":"viewer"}`, 409)
	c.mustDo("GET", "/api/v1/users/nope", "", 404)
	c.mustDo("PUT", "/api/v1/users/nope/role", `{"role":"viewer"}`, 404)
	c.mustDo("POST", "/api/v1/users/nope/tokens", `{"name":"x"}`, 404)
	c.mustDo("GET", "/api/v1/users/nope/audit", "", 404)
	c.mustDo("POST", "/api/v1/me/tokens", `{"name":"old","expires_at":"2000-01-01T00:00:00Z"}`, 400)
	c.mustDo("POST", "/api/v1/me/tokens", `{"name":""}`, 400)
	c.mustDo("POST", "/api/v1/me/tokens", `{"name":"x","expires_at":"next week"}`, 400)
	c.mustDo("DELETE", "/api/v1/me/tokens/0", "", 404)
}

// doIdem sends a request with an Idempotency-Key and returns the raw response.
func (c *client) doIdem(method, path, body, key string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set(idempotencyHeader, key)
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func TestIdempotentRetriesReplay(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	body := `{"key":"new-checkout","name":"New checkout"}`
	first := c.doIdem("POST", "/api/v1/flags", body, "retry-1")
	second := c.doIdem("POST", "/api/v1/flags", body, "retry-1")
	if first.Code != 201 || second.Code != 201 {
		t.Fatalf("codes = %d, %d", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() || second.Header().Get("Location") != "/api/v1/flags/new-checkout" {
		t.Errorf("replay differs:\n%s\n%s (Location %q)", first.Body, second.Body, second.Header().Get("Location"))
	}
	if first.Header().Get(replayedHeader) != "" || second.Header().Get(replayedHeader) != "true" {
		t.Errorf("Idempotent-Replayed = %q, %q", first.Header().Get(replayedHeader), second.Header().Get(replayedHeader))
	}
	// Applied once.
	events := c.mustDo("GET", "/api/v1/flags/new-checkout/audit", "", 200)["events"].([]any)
	if len(events) != 1 {
		t.Errorf("%d audit events, want 1", len(events))
	}

	// Without a key, a retry isn't deduplicated.
	c.mustDo("POST", "/api/v1/flags", body, 409)

	// Deletes replay too, instead of returning 404 the second time.
	if a, b := c.doIdem("DELETE", "/api/v1/flags/new-checkout", "", "del-1"), c.doIdem("DELETE", "/api/v1/flags/new-checkout", "", "del-1"); a.Code != 204 || b.Code != 204 {
		t.Errorf("delete codes = %d, %d", a.Code, b.Code)
	}
	// Client errors are replayed as well.
	if a, b := c.doIdem("POST", "/api/v1/flags", `{"key":"Bad"}`, "bad-1"), c.doIdem("POST", "/api/v1/flags", `{"key":"Bad"}`, "bad-1"); a.Code != 400 || b.Code != 400 || b.Header().Get(replayedHeader) != "true" {
		t.Errorf("bad request codes = %d, %d", a.Code, b.Code)
	}
}

func TestIdempotencyKeyMisuse(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	c.doIdem("POST", "/api/v1/flags", `{"key":"a","name":"A"}`, "k")
	if rec := c.doIdem("POST", "/api/v1/flags", `{"key":"b","name":"B"}`, "k"); rec.Code != 422 {
		t.Errorf("different body: got %d", rec.Code)
	}
	if rec := c.doIdem("PUT", "/api/v1/flags/a", `{"key":"a","name":"A"}`, "k"); rec.Code != 422 {
		t.Errorf("different route: got %d", rec.Code)
	}
	// Keys are per user.
	sam := c.as(c.newUser("sam", auth.RoleEditor))
	if rec := sam.doIdem("POST", "/api/v1/flags", `{"key":"b","name":"B"}`, "k"); rec.Code != 201 {
		t.Errorf("other user, same key: got %d", rec.Code)
	}
	for _, bad := range []string{strings.Repeat("k", 256), "has space", "tab\t"} {
		if rec := c.doIdem("POST", "/api/v1/flags", `{"key":"c","name":"C"}`, bad); rec.Code != 400 {
			t.Errorf("key %q: got %d", bad, rec.Code)
		}
	}
	// Denied requests don't consume keys.
	viewer := c.as(c.newUser("vic", auth.RoleViewer))
	if rec := viewer.doIdem("POST", "/api/v1/flags", `{"key":"e","name":"E"}`, "v"); rec.Code != 403 {
		t.Fatalf("viewer: got %d", rec.Code)
	}
	if rec, _ := c.idem.Begin(context.Background(), "vic", "v", nil); rec != nil {
		t.Errorf("denied request stored a record: %+v", rec)
	}
}

func TestIdempotencyInProgress(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	body := `{"key":"d","name":"D"}`
	// Reserve the key with this exact request's hash, as a concurrent first attempt would.
	h := sha256.New()
	io.WriteString(h, "POST /api/v1/flags\n")
	h.Write([]byte(body))
	c.idem.Begin(context.Background(), "mel", "busy", h.Sum(nil))
	if rec := c.doIdem("POST", "/api/v1/flags", body, "busy"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "in progress") {
		t.Errorf("got %d %s", rec.Code, rec.Body)
	}
}

func TestIdempotencyReleasesOnServerError(t *testing.T) {
	c := newClient(t, failingCreate{flagtest.NewMemory()})
	if rec := c.doIdem("POST", "/api/v1/flags", `{"key":"x","name":"X"}`, "k"); rec.Code != 500 {
		t.Fatalf("got %d", rec.Code)
	}
	if rec, _ := c.idem.Begin(context.Background(), "mel", "k", nil); rec != nil {
		t.Errorf("key still held after a 500: %+v", rec)
	}
}

type failingCreate struct{ *flagtest.Memory }

func (failingCreate) CreateFlag(context.Context, string, string, string, string, string) (flag.Flag, error) {
	return flag.Flag{}, errors.New("database unavailable")
}

func TestIdempotencyNeverStoresSecrets(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	for _, rt := range []struct{ path, body, field string }{
		{"/api/v1/me/tokens", `{"name":"cli"}`, "token"},
		{"/api/v1/users/mel/tokens", `{"name":"cli2"}`, "token"},
		{"/api/v1/sdk-keys", `{"environment":"prod","name":"svc"}`, "key"},
	} {
		first := c.doIdem("POST", rt.path, rt.body, "s-"+rt.path)
		var a, b map[string]any
		json.Unmarshal(first.Body.Bytes(), &a)
		secret, _ := a[rt.field].(string)
		if first.Code != 201 || secret == "" {
			t.Fatalf("%s: %d %v", rt.path, first.Code, a)
		}
		second := c.doIdem("POST", rt.path, rt.body, "s-"+rt.path)
		json.Unmarshal(second.Body.Bytes(), &b)
		if second.Code != 201 || b["id"] != a["id"] || b[rt.field] != nil {
			t.Errorf("%s replay = %d %v; want same id, no %s", rt.path, second.Code, b, rt.field)
		}
		if strings.Contains(second.Body.String(), secret) {
			t.Errorf("%s: replay leaked the secret", rt.path)
		}
	}
	// Exactly one token and one SDK key were created per route.
	if toks := c.mustDo("GET", "/api/v1/me/tokens", "", 200)["tokens"].([]any); len(toks) != 3 {
		t.Errorf("mel has %d tokens, want 3 (setup + 2)", len(toks))
	}
	if keys := c.mustDo("GET", "/api/v1/sdk-keys", "", 200)["sdk_keys"].([]any); len(keys) != 1 {
		t.Errorf("%d SDK keys, want 1", len(keys))
	}
}

func TestStewards(t *testing.T) {
	c := newClient(t, flagtest.NewMemory())
	sam := c.as(c.newUser("sam", auth.RoleEditor))
	c.newUser("ana", auth.RoleApprover)
	c.newUser("vic", auth.RoleViewer)
	c.newUser("gone", auth.RoleEditor)
	c.users.DisableUser(context.Background(), "mel", "gone")

	// The creator is the default steward; admins can name someone else.
	if f := sam.mustDo("POST", "/api/v1/flags", `{"key":"a","name":"A"}`, 201); f["steward"] != "sam" {
		t.Errorf("default steward = %v", f["steward"])
	}
	if f := c.mustDo("POST", "/api/v1/flags", `{"key":"b","name":"B","steward":"ana"}`, 201); f["steward"] != "ana" {
		t.Errorf("explicit steward = %v", f["steward"])
	}
	for _, bad := range []string{"vic", "gone", "nobody"} {
		c.mustDo("POST", "/api/v1/flags", `{"key":"c","name":"C","steward":"`+bad+`"}`, 400)
		c.mustDo("PUT", "/api/v1/flags/b/steward", `{"steward":"`+bad+`"}`, 400)
	}
	c.mustDo("PUT", "/api/v1/flags/b/steward", `{"steward":""}`, 400)

	// The current steward can hand off, then can't take it back.
	sam.mustDo("PUT", "/api/v1/flags/b/steward", `{"steward":"sam"}`, 403)
	if f := sam.mustDo("PUT", "/api/v1/flags/a/steward", `{"steward":"ana"}`, 200); f["steward"] != "ana" {
		t.Errorf("after handoff = %v", f["steward"])
	}
	sam.mustDo("PUT", "/api/v1/flags/a/steward", `{"steward":"sam"}`, 403)
	c.mustDo("PUT", "/api/v1/flags/nope/steward", `{"steward":"sam"}`, 404)

	events := c.mustDo("GET", "/api/v1/flags/a/audit", "", 200)["events"].([]any)
	last := events[len(events)-1].(map[string]any)
	if last["action"] != "flag.steward_changed" || last["actor"] != "sam" ||
		last["before"].(map[string]any)["steward"] != "sam" || last["after"].(map[string]any)["steward"] != "ana" {
		t.Errorf("steward audit event = %v", last)
	}

	// Filters: by handle, and "none" for unassigned or disabled stewards.
	c.mustDo("POST", "/api/v1/flags", `{"key":"d","name":"D","steward":"sam"}`, 201)
	c.users.DisableUser(context.Background(), "mel", "sam")
	keys := func(q string) string {
		var out []string
		for _, f := range c.mustDo("GET", "/api/v1/flags"+q, "", 200)["flags"].([]any) {
			out = append(out, f.(map[string]any)["key"].(string))
		}
		return strings.Join(out, ",")
	}
	for q, want := range map[string]string{"": "a,b,d", "?steward=ana": "a,b", "?steward=sam": "d", "?steward=none": "d", "?steward=zed": ""} {
		if got := keys(q); got != want {
			t.Errorf("GET /flags%s = %q, want %q", q, got, want)
		}
	}
}
