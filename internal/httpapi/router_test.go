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

	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
)

const testKey = "test-key-0123456789abcdef0123456789"

type client struct {
	t *testing.T
	h http.Handler
}

func newClient(t *testing.T, store flag.Store) *client {
	return &client{t, NewRouter(store, testKey, slog.New(slog.NewTextHandler(io.Discard, nil)))}
}

// do sends an authenticated request and decodes a JSON response into a map.
func (c *client) do(method, path, body string) (int, map[string]any) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testKey)
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
	h := newClient(t, flagtest.NewMemory()).h
	for _, header := range []string{"", "Bearer", "Bearer wrong", "Basic " + testKey, testKey, "Bearer " + testKey + "x"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: got %d, want 401", header, rec.Code)
		}
	}
	// Unknown API paths also require auth, so they don't reveal what exists.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown path: got %d, want 401", rec.Code)
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
		if ev["actor"] != actor {
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
