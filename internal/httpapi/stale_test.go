package httpapi

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/auth/authtest"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
	"github.com/Melmonster13/featuresteward/internal/idempotency/idemtest"
)

// staleClient's clock can be moved forward.
func staleClient(t *testing.T) (*client, *flagtest.Memory, *time.Time) {
	flags, users, idem := flagtest.NewMemory(), authtest.NewMemory(), idemtest.NewMemory()
	now := time.Now()
	c := &client{t: t, users: users, idem: idem,
		h: NewRouter(flags, users, idem, slog.New(slog.NewTextHandler(io.Discard, nil)),
			WithStaleAfter(30*24*time.Hour), withClock(func() time.Time { return now }))}
	c.token = c.newUser("mel", auth.RoleAdmin)
	return c, flags, &now
}

func staleReason(t *testing.T, f map[string]any) string {
	t.Helper()
	if f["stale"] == nil {
		return ""
	}
	return f["stale"].(map[string]any)["reason"].(string)
}

func TestStaleFlags(t *testing.T) {
	c, flags, now := staleClient(t)
	f := c.mustDo("POST", "/api/v1/flags", `{"key":"old-banner","name":"Old banner"}`, 201)
	if f["stale"] != nil || f["permanent_reason"] != nil || len(f["activity"].(map[string]any)) != 3 {
		t.Fatalf("new flag = %v", f)
	}
	c.mustDo("POST", "/api/v1/flags", `{"key":"launched","name":"Launched"}`, 201)
	for _, env := range []string{"dev", "staging"} {
		c.mustDo("PUT", "/api/v1/flags/launched/environments/"+env, `{"enabled":true,"rollout_percentage":100}`, 200)
	}
	c.mustDo("PUT", "/api/v1/flags/launched/environments/prod", `{"enabled":true,"rollout_percentage":100,"reason":"launch"}`, 200)
	c.mustDo("POST", "/api/v1/flags", `{"key":"rolling-out","name":"Rolling out"}`, 201)
	c.mustDo("PUT", "/api/v1/flags/rolling-out/environments/dev", `{"enabled":true,"rollout_percentage":40}`, 200)

	if list := c.mustDo("GET", "/api/v1/flags?stale=true", "", 200)["flags"].([]any); len(list) != 0 {
		t.Fatalf("stale today = %v", list)
	}

	// 31 days later. launched and rolling-out were evaluated yesterday;
	// old-banner never was.
	*now = now.Add(31 * 24 * time.Hour)
	yesterday := now.Add(-24 * time.Hour)
	var seen []flag.Evaluation
	for _, key := range []string{"launched", "rolling-out"} {
		seen = append(seen, flag.Evaluation{Flag: key, Environment: "prod", At: yesterday})
	}
	flags.RecordEvaluations(context.Background(), seen)

	list := c.mustDo("GET", "/api/v1/flags?stale=true", "", 200)["flags"].([]any)
	got := map[string]string{}
	for _, x := range list {
		f := x.(map[string]any)
		got[f["key"].(string)] = staleReason(t, f)
	}
	if len(got) != 2 || got["old-banner"] != "unused" || got["launched"] != "always_on" {
		t.Fatalf("stale = %v", got)
	}
	f = c.mustDo("GET", "/api/v1/flags/launched", "", 200)
	st := f["stale"].(map[string]any)
	if !strings.Contains(st["suggestion"].(string), "keep the new code") || st["since"] == nil {
		t.Errorf("stale = %v", st)
	}

	// A pending request, or a permanent mark, means it isn't stale.
	c.mustDo("POST", "/api/v1/flags/launched/environments/prod/requests", `{"enabled":false,"rollout_percentage":100}`, 201)
	if staleReason(t, c.mustDo("GET", "/api/v1/flags/launched", "", 200)) != "" {
		t.Error("flag with a pending request reported stale")
	}
	f = c.mustDo("PUT", "/api/v1/flags/old-banner/permanent", `{"reason":"  seasonal banner  "}`, 200)
	if f["permanent_reason"] != "seasonal banner" || f["stale"] != nil {
		t.Errorf("after marking permanent = %v", f)
	}
	f = c.mustDo("PUT", "/api/v1/flags/old-banner/permanent", `{"reason":""}`, 200)
	if f["permanent_reason"] != nil || staleReason(t, f) != "unused" {
		t.Errorf("after clearing = %v", f)
	}

	// Filters combine, and bad values are refused.
	c.newUser("sam", auth.RoleEditor)
	c.mustDo("PUT", "/api/v1/flags/old-banner/steward", `{"steward":"sam"}`, 200)
	if list := c.mustDo("GET", "/api/v1/flags?stale=true&steward=sam", "", 200)["flags"].([]any); len(list) != 1 {
		t.Errorf("sam's stale flags = %v", list)
	}
	if code, out := c.do("GET", "/api/v1/flags?stale=yes", ""); code != 400 || !strings.Contains(out["error"].(string), "stale must be true") {
		t.Errorf("stale=yes = %d %v", code, out)
	}
}

func TestPermanentNeedsStewardOrAdmin(t *testing.T) {
	c, _, _ := staleClient(t)
	kim := c.as(c.newUser("kim", auth.RoleEditor))
	lee := c.as(c.newUser("lee", auth.RoleEditor))
	c.mustDo("POST", "/api/v1/flags", `{"key":"kill-switch","name":"Kill switch","steward":"kim"}`, 201)
	if code, out := lee.do("PUT", "/api/v1/flags/kill-switch/permanent", `{"reason":"mine now"}`); code != 403 ||
		!strings.Contains(out["error"].(string), "steward") {
		t.Errorf("non-steward editor = %d %v", code, out)
	}
	kim.mustDo("PUT", "/api/v1/flags/kill-switch/permanent", `{"reason":"payments kill switch"}`, 200)
	events := c.mustDo("GET", "/api/v1/flags/kill-switch/audit", "", 200)["events"].([]any)
	last := events[len(events)-1].(map[string]any)
	if last["action"] != "flag.permanent_changed" || last["actor"] != "kim" {
		t.Errorf("audit = %v", last)
	}
	if code, _ := kim.do("PUT", "/api/v1/flags/kill-switch/permanent", `{"reason":"`+strings.Repeat("x", 501)+`"}`); code != 400 {
		t.Errorf("long reason = %d", code)
	}
}
