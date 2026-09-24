package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/auth/authtest"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
	"github.com/Melmonster13/featuresteward/internal/httpapi"
	"github.com/Melmonster13/featuresteward/internal/idempotency/idemtest"
)

// harness is a real API server on in-memory stores plus a config dir.
type harness struct {
	t      *testing.T
	url    string
	users  *authtest.Memory
	config string // XDG_CONFIG_HOME
	env    map[string]string
}

func newHarness(t *testing.T) *harness {
	users := authtest.NewMemory()
	srv := httptest.NewServer(httpapi.NewRouter(flagtest.NewMemory(), users, idemtest.NewMemory(),
		slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	h := &harness{t: t, url: srv.URL, users: users, config: t.TempDir()}
	h.env = map[string]string{"XDG_CONFIG_HOME": h.config}
	return h
}

// token creates a user and returns an API token for them.
func (h *harness) token(handle string, role auth.Role) string {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.users.CreateUser(ctx, "system", handle, "", role); err != nil {
		h.t.Fatal(err)
	}
	secret, hash, prefix := auth.NewSecret(auth.TokenPrefix)
	if _, err := h.users.CreateToken(ctx, "system", handle, "test", hash, prefix, nil); err != nil {
		h.t.Fatal(err)
	}
	return secret
}

// stew runs a command with stdin and returns the exit code and output.
func (h *harness) stew(stdin string, args ...string) (int, string, string) {
	h.t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, func(k string) string { return h.env[k] },
		strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func (h *harness) mustStew(stdin string, args ...string) string {
	h.t.Helper()
	code, out, errOut := h.stew(stdin, args...)
	if code != 0 {
		h.t.Fatalf("stew %v = %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out
}

// api calls the API directly with a token.
func (h *harness) api(token, method, path, body string) int {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.url+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestHelpAndUsageErrors(t *testing.T) {
	h := newHarness(t)
	if code, out, _ := h.stew("", "help"); code != 0 || !strings.Contains(out, "stew login") {
		t.Errorf("help = %d %q", code, out)
	}
	if code, _, errOut := h.stew("", "frobnicate"); code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Errorf("unknown command = %d %q", code, errOut)
	}
	if code, _, _ := h.stew("", "list", "--nope"); code != 2 {
		t.Errorf("bad flag = %d", code)
	}
	if code, _, _ := h.stew("", "whoami", "extra"); code != 2 {
		t.Errorf("extra arg = %d", code)
	}
	if code, _, errOut := h.stew("", "login"); code != 2 || !strings.Contains(errOut, "--url is required") {
		t.Errorf("login without url = %d %q", code, errOut)
	}
}

func TestLoginWhoamiLogout(t *testing.T) {
	h := newHarness(t)
	tok := h.token("mel", auth.RoleAdmin)

	if code, _, errOut := h.stew("", "whoami"); code != 1 || !strings.Contains(errOut, "not logged in") {
		t.Errorf("whoami before login = %d %q", code, errOut)
	}

	out := h.mustStew(tok+"\n", "login", "--url", h.url)
	if !strings.Contains(out, "as mel (admin)") {
		t.Errorf("login output = %q", out)
	}
	path := filepath.Join(h.config, "stew", "config.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm() != 0o700 {
		t.Errorf("config dir mode = %v; want 0700", dir.Mode().Perm())
	}
	var saved config
	data, _ := os.ReadFile(path)
	if json.Unmarshal(data, &saved); saved.URL != h.url || saved.Token != tok {
		t.Errorf("saved = %+v", saved)
	}
	// The token never appears in output.
	if strings.Contains(out, tok) {
		t.Error("login printed the token")
	}

	if out := h.mustStew("", "whoami"); !strings.Contains(out, "mel (admin) at "+h.url) {
		t.Errorf("whoami = %q", out)
	}

	// logout revokes the token server-side.
	h.mustStew("", "logout")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config still exists: %v", err)
	}
	if code := h.api(tok, "GET", "/api/v1/me", ""); code != 401 {
		t.Errorf("token after logout: got %d, want 401", code)
	}
	// Logging out again is harmless.
	h.mustStew("", "logout")
}

func TestLogoutKeepToken(t *testing.T) {
	h := newHarness(t)
	tok := h.token("mel", auth.RoleAdmin)
	h.mustStew(tok, "login", "--url", h.url)
	h.mustStew("", "logout", "--keep-token")
	if code := h.api(tok, "GET", "/api/v1/me", ""); code != 200 {
		t.Errorf("kept token: got %d, want 200", code)
	}
}

func TestLoginRejects(t *testing.T) {
	h := newHarness(t)
	h.token("mel", auth.RoleAdmin)
	path := filepath.Join(h.config, "stew", "config.json")

	if code, _, errOut := h.stew("fs_wrong\n", "login", "--url", h.url); code != 1 || !strings.Contains(errOut, "checking token") {
		t.Errorf("bad token = %d %q", code, errOut)
	}
	if code, _, errOut := h.stew("\n", "login", "--url", h.url); code != 1 || !strings.Contains(errOut, "no token") {
		t.Errorf("empty token = %d %q", code, errOut)
	}
	for _, u := range []string{"http://flags.example.com", "ftp://x", "not a url", "localhost:8080"} {
		if code, _, _ := h.stew("fs_x\n", "login", "--url", u); code != 1 {
			t.Errorf("url %q = %d, want 1", u, code)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a failed login saved config: %v", err)
	}
}

func TestEnvironmentOverrides(t *testing.T) {
	h := newHarness(t)
	h.env["STEW_URL"] = h.url
	h.env["STEW_TOKEN"] = h.token("sam", auth.RoleEditor)
	if out := h.mustStew("", "whoami"); !strings.Contains(out, "sam (editor)") {
		t.Errorf("whoami = %q", out)
	}
	h.env["STEW_URL"] = "http://flags.example.com"
	if code, _, errOut := h.stew("", "whoami"); code != 1 || !strings.Contains(errOut, "plain http") {
		t.Errorf("insecure env url = %d %q", code, errOut)
	}
}

func TestWorldReadableConfigWarns(t *testing.T) {
	h := newHarness(t)
	h.mustStew(h.token("mel", auth.RoleAdmin), "login", "--url", h.url)
	os.Chmod(filepath.Join(h.config, "stew", "config.json"), 0o644)
	if _, _, errOut := h.stew("", "whoami"); !strings.Contains(errOut, "readable by other users") {
		t.Errorf("no warning: %q", errOut)
	}
}

func TestList(t *testing.T) {
	h := newHarness(t)
	tok := h.token("mel", auth.RoleAdmin)
	h.token("sam", auth.RoleEditor)
	h.mustStew(tok, "login", "--url", h.url)

	if _, _, errOut := h.stew("", "list"); !strings.Contains(errOut, "No flags") {
		t.Errorf("empty list stderr = %q", errOut)
	}
	h.api(tok, "POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout"}`)
	h.api(tok, "PUT", "/api/v1/flags/new-checkout/environments/prod",
		`{"enabled":true,"rollout_percentage":25,"rules":[{"attribute":"group","values":["staff"],"serve":true}]}`)
	h.api(tok, "PUT", "/api/v1/flags/new-checkout/environments/dev", `{"enabled":true,"rollout_percentage":100}`)
	h.api(tok, "POST", "/api/v1/flags", `{"key":"dark-mode","name":"Dark mode","steward":"sam"}`)

	out := h.mustStew("", "list")
	want := `KEY           DEV  PROD         STAGING  STEWARD
dark-mode     off  off          off      @sam
new-checkout  on   25% +1 rule  off      @mel
`
	if out != want {
		t.Errorf("list =\n%s\nwant\n%s", out, want)
	}

	if out := h.mustStew("", "list", "--env", "prod"); !strings.Contains(out, "KEY           PROD         STEWARD") ||
		strings.Contains(out, "DEV") {
		t.Errorf("list --env prod =\n%s", out)
	}
	if out := h.mustStew("", "list", "--steward", "sam"); strings.Contains(out, "new-checkout") || !strings.Contains(out, "dark-mode") {
		t.Errorf("list --steward sam =\n%s", out)
	}
	if code, _, errOut := h.stew("", "list", "--env", "qa"); code != 1 || !strings.Contains(errOut, `unknown environment "qa"`) {
		t.Errorf("unknown env = %d %q", code, errOut)
	}
}
