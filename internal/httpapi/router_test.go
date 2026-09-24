package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/auth/authtest"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
)

type client struct {
	t     *testing.T
	h     http.Handler
	users *authtest.Memory
	token string // sent as the bearer credential
}

// newClient returns a client authenticated as admin "mel".
func newClient(t *testing.T, flags flag.Store) *client {
	users := authtest.NewMemory()
	c := &client{t: t, users: users, h: NewRouter(flags, users, slog.New(slog.NewTextHandler(io.Discard, nil)))}
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
	if c.token != "" {
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
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
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
	c.mustDo("PUT", "/api/v1/flags/new-checkout/environments/prod", `{"enabled":true,"rollout_percentage":100}`, 200)
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
		`{"enabled":true,"rollout_percentage":25,"rules":[{"attribute":"group","values":["staff"],"serve":true}]}`, 200)
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
		`{"enabled":true,"rollout_percentage":25,"rules":[{"attribute":"group","values":["staff"],"serve":true}]}`, 200)

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
