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
	"github.com/Melmonster13/featuresteward/internal/client"
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
	if code, out, _ := h.stew("", "version"); code != 0 || !strings.HasPrefix(out, "stew ") {
		t.Errorf("version = %d %q", code, out)
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

	if code, _, errOut := h.stew("", "whoami"); code != 3 || !strings.Contains(errOut, "not logged in") {
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

	if code, _, errOut := h.stew("fs_wrong\n", "login", "--url", h.url); code != 3 || !strings.Contains(errOut, "checking token") {
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
		`{"enabled":true,"rollout_percentage":25,"rules":[{"attribute":"group","values":["staff"],"serve":true}],"reason":"test"}`)
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
	if code, _, errOut := h.stew("", "list", "--env", "qa"); code != 4 || !strings.Contains(errOut, `unknown environment "qa"`) {
		t.Errorf("unknown env = %d %q", code, errOut)
	}
}

func TestFlagCommands(t *testing.T) {
	h := newHarness(t)
	tok := h.token("mel", auth.RoleAdmin)
	h.token("sam", auth.RoleEditor)
	h.mustStew(tok, "login", "--url", h.url)

	// Flags may come after the positional argument.
	out := h.mustStew("", "create", "new-checkout", "--name", "New checkout", "--description", "Faster checkout")
	if !strings.Contains(out, "Created new-checkout (steward @mel)") {
		t.Errorf("create = %q", out)
	}
	if code, _, errOut := h.stew("", "create", "new-checkout", "--name", "Again"); code != 1 || !strings.Contains(errOut, "stew create:") {
		t.Errorf("duplicate create = %d %q", code, errOut)
	}

	// toggle and rollout each change one field and keep the rest.
	h.api(tok, "PUT", "/api/v1/flags/new-checkout/environments/staging",
		`{"enabled":false,"rollout_percentage":25,"rules":[{"attribute":"group","values":["staff"],"serve":true}]}`)
	if out := h.mustStew("", "toggle", "new-checkout", "staging", "on"); out != "new-checkout in staging: 25% +1 rule\n" {
		t.Errorf("toggle = %q", out)
	}
	if out := h.mustStew("", "rollout", "new-checkout", "staging", "50%"); out != "new-checkout in staging: 50% +1 rule\n" {
		t.Errorf("rollout = %q", out)
	}
	h.mustStew("", "rollout", "new-checkout", "dev", "0")
	if _, _, errOut := h.stew("", "toggle", "new-checkout", "dev", "on"); !strings.Contains(errOut, "rollout is 0%") {
		t.Errorf("no 0%% note: %q", errOut)
	}

	h.mustStew("", "steward", "new-checkout", "@sam")
	out = h.mustStew("", "status", "new-checkout")
	for _, want := range []string{
		"new-checkout  New checkout\n  Faster checkout\nSteward: @sam\n",
		"dev               on 0%   -\n",
		"prod (protected)  off     -\n",
		"staging           on 50%  group in [staff] → on\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}

	if code, _, errOut := h.stew("", "archive", "new-checkout"); code != 2 || !strings.Contains(errOut, "--yes") {
		t.Errorf("archive without --yes = %d %q", code, errOut)
	}
	h.mustStew("", "archive", "new-checkout", "--yes")
	if out := h.mustStew("", "status", "new-checkout"); !strings.Contains(out, "Archived: ") {
		t.Errorf("status after archive:\n%s", out)
	}
	if code, _, errOut := h.stew("", "status", "nope"); code != 4 || !strings.Contains(errOut, `flag "nope" not found`) {
		t.Errorf("status of unknown flag = %d %q", code, errOut)
	}
}

func TestFlagCommandPermissions(t *testing.T) {
	h := newHarness(t)
	admin := h.token("mel", auth.RoleAdmin)
	h.api(admin, "POST", "/api/v1/flags", `{"key":"dark-mode","name":"Dark mode"}`)
	h.env["STEW_URL"] = h.url
	h.env["STEW_TOKEN"] = h.token("sam", auth.RoleEditor)

	h.mustStew("", "toggle", "dark-mode", "dev", "on")
	if out := h.mustStew("", "toggle", "dark-mode", "prod", "on"); !strings.Contains(out, "Requested #1: dark-mode in prod → on.") {
		t.Errorf("editor toggling prod = %q", out)
	}
	if code, _, errOut := h.stew("", "rollout", "dark-mode", "prod", "5", "--emergency", "outage"); code != 3 || !strings.Contains(errOut, "change request") {
		t.Errorf("editor emergency = %d %q", code, errOut)
	}
	if code, _, errOut := h.stew("", "steward", "dark-mode", "sam"); code != 3 || !strings.Contains(errOut, "current steward") {
		t.Errorf("editor taking a flag = %d %q", code, errOut)
	}
	if code, _, _ := h.stew("", "archive", "dark-mode", "--yes"); code != 3 {
		t.Errorf("editor archiving = %d, want 3", code)
	}
}

func TestFlagCommandUsage(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"status"},
		{"status", "a", "b"},
		{"create", "x"},
		{"toggle", "x", "dev"},
		{"toggle", "x", "dev", "maybe"},
		{"rollout", "x", "dev", "101"},
		{"rollout", "x", "dev", "half"},
		{"steward", "x"},
	} {
		if code, _, _ := h.stew("", args...); code != 2 {
			t.Errorf("stew %v = %d, want 2", args, code)
		}
	}
}

func TestJSONOutput(t *testing.T) {
	h := newHarness(t)
	tok := h.token("mel", auth.RoleAdmin)
	h.token("sam", auth.RoleEditor)
	h.mustStew(tok, "login", "--url", h.url)

	decodeJSON := func(out string, v any) {
		t.Helper()
		if err := json.Unmarshal([]byte(out), v); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
	}

	var me map[string]string
	out := h.mustStew("", "whoami", "--json")
	decodeJSON(out, &me)
	if me["handle"] != "mel" || me["role"] != "admin" || me["url"] != h.url {
		t.Errorf("whoami --json = %v", me)
	}
	if strings.Contains(out, tok) {
		t.Error("whoami --json printed the token")
	}

	var list struct {
		Flags []client.Flag `json:"flags"`
	}
	if out := h.mustStew("", "list", "--json"); strings.TrimSpace(out) != `{
  "flags": []
}` {
		t.Errorf("empty list --json = %q", out)
	}

	var f client.Flag
	decodeJSON(h.mustStew("", "create", "dark-mode", "--name", "Dark mode", "--steward", "@sam", "--json"), &f)
	if f.Key != "dark-mode" || f.Steward == nil || *f.Steward != "sam" {
		t.Errorf("create --json = %+v", f)
	}
	decodeJSON(h.mustStew("", "rollout", "dark-mode", "dev", "30", "--json"), &f)
	decodeJSON(h.mustStew("", "toggle", "dark-mode", "dev", "on", "--json"), &f)
	if dev := f.Environments["dev"]; !dev.Enabled || dev.RolloutPercentage != 30 {
		t.Errorf("toggle --json dev = %+v", dev)
	}
	decodeJSON(h.mustStew("", "steward", "dark-mode", "mel", "--json"), &f)
	if *f.Steward != "mel" {
		t.Errorf("steward --json = %v", *f.Steward)
	}
	decodeJSON(h.mustStew("", "status", "dark-mode", "--json"), &f)
	if f.Name != "Dark mode" || len(f.Environments) != 3 {
		t.Errorf("status --json = %+v", f)
	}

	decodeJSON(h.mustStew("", "list", "--env", "dev", "--json"), &list)
	if len(list.Flags) != 1 || len(list.Flags[0].Environments) != 1 || !list.Flags[0].Environments["dev"].Enabled {
		t.Errorf("list --env dev --json = %+v", list)
	}
}

func TestExitCodes(t *testing.T) {
	h := newHarness(t)
	h.env["STEW_URL"] = h.url
	for _, c := range []struct {
		token string
		args  []string
		want  int
	}{
		{"", []string{"status", "x"}, 3},                                             // not logged in
		{"fs_wrong", []string{"status", "x"}, 3},                                     // bad token
		{h.token("vic", auth.RoleViewer), []string{"create", "x", "--name", "X"}, 3}, // not allowed
		{h.token("mel", auth.RoleAdmin), []string{"status", "x"}, 4},                 // no such flag
		{h.env["STEW_TOKEN"], []string{"toggle", "x"}, 2},                            // usage
	} {
		h.env["STEW_TOKEN"] = c.token
		if code, _, errOut := h.stew("", c.args...); code != c.want {
			t.Errorf("stew %v = %d, want %d (%s)", c.args, code, c.want, errOut)
		}
	}
}

func TestProdChangesNeedApproval(t *testing.T) {
	h := newHarness(t)
	admin := h.token("mel", auth.RoleAdmin)
	sam := h.token("sam", auth.RoleEditor)
	ana := h.token("ana", auth.RoleApprover)
	h.api(admin, "POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout","steward":"sam"}`)
	h.env["STEW_URL"] = h.url
	as := func(token string) { h.env["STEW_TOKEN"] = token }

	as(sam)
	out := h.mustStew("", "rollout", "new-checkout", "prod", "25", "--reason", "launch to a quarter")
	if !strings.Contains(out, "Requested #1: new-checkout in prod → off (25% when on).") || !strings.Contains(out, "stew requests") {
		t.Errorf("rollout = %q", out)
	}
	h.stew("", "cancel", "1")
	out = h.mustStew("", "toggle", "new-checkout", "prod", "on", "--reason", "launch")
	if !strings.Contains(out, "Requested #2: new-checkout in prod → on.") {
		t.Errorf("toggle = %q", out)
	}
	if status := h.mustStew("", "status", "new-checkout"); !strings.Contains(status, "prod (protected)  off") {
		t.Errorf("a request changed prod:\n%s", status)
	}
	if list := h.mustStew("", "requests"); !strings.Contains(list, "2   new-checkout  prod  @sam  off → on  pending  cancel") {
		t.Errorf("sam's requests =\n%s", list)
	}
	// Nobody approves their own request, even the steward.
	if code, _, errOut := h.stew("", "approve", "2"); code != 3 || !strings.Contains(errOut, "your own") {
		t.Errorf("self-approve = %d %q", code, errOut)
	}

	as(ana)
	if list := h.mustStew("", "requests"); !strings.Contains(list, "pending  review") {
		t.Errorf("ana's requests =\n%s", list)
	}
	if out := h.mustStew("", "approve", "#2", "--comment", "ship it"); out != "Approved #2: new-checkout in prod is now on.\n" {
		t.Errorf("approve = %q", out)
	}
	if code, _, errOut := h.stew("", "approve", "2"); code != 1 || !strings.Contains(errOut, "already approved") {
		t.Errorf("approve twice = %d %q", code, errOut)
	}
	if _, _, errOut := h.stew("", "requests"); !strings.Contains(errOut, "No pending change requests.") {
		t.Errorf("empty requests = %q", errOut)
	}
	var all struct {
		Requests []client.ChangeRequest `json:"requests"`
	}
	json.Unmarshal([]byte(h.mustStew("", "requests", "--all", "--json")), &all)
	if len(all.Requests) != 2 || all.Requests[0].Status != "approved" || all.Requests[0].ReviewComment != "ship it" ||
		all.Requests[1].Status != "cancelled" {
		t.Errorf("all requests = %+v", all.Requests)
	}

	// Turning prod off is immediate for editors.
	as(sam)
	if out := h.mustStew("", "toggle", "new-checkout", "prod", "off"); out != "new-checkout in prod: off\n" {
		t.Errorf("kill switch = %q", out)
	}
	// Admins can apply an emergency change.
	as(admin)
	if out := h.mustStew("", "rollout", "new-checkout", "prod", "5", "--emergency", "outage"); out != "new-checkout in prod: off\n" {
		t.Errorf("emergency = %q", out)
	}
	if out := h.mustStew("", "toggle", "new-checkout", "prod", "on", "--emergency", "outage"); out != "new-checkout in prod: 5%\n" {
		t.Errorf("emergency toggle = %q", out)
	}

	// Rejecting leaves prod alone.
	as(sam)
	h.mustStew("", "rollout", "new-checkout", "prod", "50")
	as(ana)
	if out := h.mustStew("", "reject", "3", "--comment", "not yet"); out != "Rejected #3. new-checkout in prod stays 5%.\n" {
		t.Errorf("reject = %q", out)
	}
}

func TestRequestCommandErrors(t *testing.T) {
	h := newHarness(t)
	h.env["STEW_URL"] = h.url
	h.env["STEW_TOKEN"] = h.token("ana", auth.RoleApprover)
	for _, args := range [][]string{{"approve"}, {"approve", "abc"}, {"reject", "0"}, {"cancel", "1", "2"}, {"requests", "extra"}} {
		if code, _, _ := h.stew("", args...); code != 2 {
			t.Errorf("stew %v = %d, want 2", args, code)
		}
	}
	if code, _, errOut := h.stew("", "approve", "99"); code != 4 || !strings.Contains(errOut, "request #99 not found") {
		t.Errorf("unknown request = %d %q", code, errOut)
	}
}
