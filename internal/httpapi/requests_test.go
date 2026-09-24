package httpapi

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
)

// approvalSetup returns admin mel, editor sam (the requester), editor kim
// (new-checkout's steward), editor lee, and approver ana.
func approvalSetup(t *testing.T) (mel, sam, kim, lee, ana *client, flags *flagtest.Memory) {
	flags = flagtest.NewMemory()
	mel = newClient(t, flags)
	sam = mel.as(mel.newUser("sam", auth.RoleEditor))
	kim = mel.as(mel.newUser("kim", auth.RoleEditor))
	lee = mel.as(mel.newUser("lee", auth.RoleEditor))
	ana = mel.as(mel.newUser("ana", auth.RoleApprover))
	mel.mustDo("POST", "/api/v1/flags", `{"key":"new-checkout","name":"New checkout","steward":"kim"}`, 201)
	return
}

func requestPath(r map[string]any, action string) string {
	return "/api/v1/requests/" + strconv.FormatFloat(r["id"].(float64), 'f', 0, 64) + action
}

func prodState(c *client) map[string]any {
	f := c.mustDo("GET", "/api/v1/flags/new-checkout", "", 200)
	return f["environments"].(map[string]any)["prod"].(map[string]any)
}

func TestChangeRequestFlow(t *testing.T) {
	_, sam, _, lee, ana, _ := approvalSetup(t)

	r := sam.mustDo("POST", "/api/v1/flags/new-checkout/environments/prod/requests",
		`{"enabled":true,"rollout_percentage":25,"reason":"launch to a quarter"}`, 201)
	if r["status"] != "pending" || r["requested_by"] != "sam" || r["reason"] != "launch to a quarter" ||
		r["reviewed_by"] != nil || r["base"].(map[string]any)["enabled"] != false {
		t.Fatalf("created = %v", r)
	}
	if prodState(sam)["enabled"] != false {
		t.Fatal("requesting changed prod")
	}
	list := sam.mustDo("GET", "/api/v1/requests?status=pending", "", 200)["requests"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["flag"] != "new-checkout" {
		t.Fatalf("pending = %v", list)
	}

	// Not the requester, and not an editor who isn't the steward.
	if _, out := sam.do("POST", requestPath(r, "/approve"), ""); !strings.Contains(out["error"].(string), "steward") {
		t.Errorf("requester approving: %v", out)
	}
	lee.mustDo("POST", requestPath(r, "/approve"), "", 403)

	got := ana.mustDo("POST", requestPath(r, "/approve"), `{"comment":" ship it "}`, 200)
	if got["status"] != "approved" || got["reviewed_by"] != "ana" || got["review_comment"] != "ship it" || got["resolved_at"] == nil {
		t.Errorf("approved = %v", got)
	}
	if p := prodState(sam); p["enabled"] != true || p["rollout_percentage"] != 25.0 {
		t.Errorf("prod after approval = %v", p)
	}
	if _, out := ana.do("POST", requestPath(r, "/approve"), ""); !strings.Contains(out["error"].(string), "already approved") {
		t.Errorf("approving twice: %v", out)
	}
}

func TestStewardCanReview(t *testing.T) {
	_, sam, kim, _, _, _ := approvalSetup(t)
	r := sam.mustDo("POST", "/api/v1/flags/new-checkout/environments/prod/requests", `{"enabled":true,"rollout_percentage":100}`, 201)
	kim.mustDo("POST", requestPath(r, "/reject"), `{"comment":"wait until Monday"}`, 200)
	if prodState(sam)["enabled"] != false {
		t.Error("rejecting changed prod")
	}
	// A steward who requests can't approve their own request either.
	r = kim.mustDo("POST", "/api/v1/flags/new-checkout/environments/prod/requests", `{"enabled":true,"rollout_percentage":100}`, 201)
	if _, out := kim.do("POST", requestPath(r, "/approve"), ""); !strings.Contains(out["error"].(string), "your own") {
		t.Errorf("steward approving own request: %v", out)
	}
}

func TestCancelRequest(t *testing.T) {
	_, sam, kim, _, _, _ := approvalSetup(t)
	r := sam.mustDo("POST", "/api/v1/flags/new-checkout/environments/prod/requests", `{"enabled":true,"rollout_percentage":100}`, 201)
	kim.mustDo("POST", requestPath(r, "/cancel"), "", 403)
	if got := sam.mustDo("POST", requestPath(r, "/cancel"), "", 200); got["status"] != "cancelled" {
		t.Errorf("cancelled = %v", got)
	}
}

func TestKillSwitchAndEmergencies(t *testing.T) {
	mel, sam, _, _, _, _ := approvalSetup(t)
	prod := "/api/v1/flags/new-checkout/environments/prod"
	on := `{"enabled":true,"rollout_percentage":25,"rules":[{"attribute":"group","values":["staff"],"serve":true}]}`

	// Editors can't change prod directly...
	if _, out := sam.do("PUT", prod, on); !strings.Contains(out["error"].(string), "change request") ||
		!strings.Contains(out["error"].(string), "off is allowed") {
		t.Errorf("editor turning on: %v", out)
	}
	// ...and admins need a reason.
	if _, out := mel.do("PUT", prod, on); !strings.Contains(out["error"].(string), "reason") {
		t.Errorf("admin without reason: %v", out)
	}
	mel.mustDo("PUT", prod, strings.TrimSuffix(on, "}")+`,"reason":"  checkout outage  "}`, 200)

	// Kill switch: anyone who can edit may turn it off, changing nothing else.
	sam.mustDo("PUT", prod, `{"enabled":false,"rollout_percentage":0,"rules":[]}`, 403)
	sam.mustDo("PUT", prod, `{"enabled":false,"rollout_percentage":25,"rules":[{"attribute":"group","values":["staff"],"serve":true}]}`, 200)
	if prodState(sam)["enabled"] != false {
		t.Fatal("kill switch didn't turn prod off")
	}

	events := mel.mustDo("GET", "/api/v1/flags/new-checkout/audit", "", 200)["events"].([]any)
	emergency := events[1].(map[string]any)
	if emergency["actor"] != "mel" || emergency["after"].(map[string]any)["emergency_reason"] != "checkout outage" {
		t.Errorf("emergency event = %v", emergency)
	}
	killed := events[2].(map[string]any)
	if killed["actor"] != "sam" || killed["after"].(map[string]any)["emergency_reason"] != nil {
		t.Errorf("kill switch event = %v", killed)
	}

	// Reasons only matter in protected environments.
	mel.mustDo("PUT", "/api/v1/flags/new-checkout/environments/dev", `{"enabled":true,"rollout_percentage":100,"reason":"x"}`, 200)
	events = mel.mustDo("GET", "/api/v1/flags/new-checkout/audit", "", 200)["events"].([]any)
	if dev := events[len(events)-1].(map[string]any); dev["after"].(map[string]any)["emergency_reason"] != nil {
		t.Errorf("dev change recorded a reason: %v", dev)
	}
	// Invalid settings are explained before permissions.
	if _, out := sam.do("PUT", prod, `{"enabled":true,"rollout_percentage":101}`); !strings.Contains(out["error"].(string), "0-100") {
		t.Errorf("invalid prod change: %v", out)
	}
}

func TestStaleAndExpiredRequests(t *testing.T) {
	mel, sam, _, _, ana, flags := approvalSetup(t)
	r := sam.mustDo("POST", "/api/v1/flags/new-checkout/environments/prod/requests", `{"enabled":true,"rollout_percentage":25}`, 201)
	mel.mustDo("PUT", "/api/v1/flags/new-checkout/environments/prod", `{"enabled":false,"rollout_percentage":0,"reason":"reset"}`, 200)
	if _, out := ana.do("POST", requestPath(r, "/approve"), ""); !strings.Contains(out["error"].(string), "has changed since") {
		t.Errorf("stale approve: %v", out)
	}
	sam.mustDo("POST", requestPath(r, "/cancel"), "", 200)

	old, _ := flags.CreateChangeRequest(context.Background(), "sam", "new-checkout", "prod",
		flag.EnvConfig{Enabled: true, RolloutPercentage: 5}, "", time.Now().Add(-time.Minute))
	if code, out := ana.do("POST", "/api/v1/requests/"+strconv.FormatInt(old.ID, 10)+"/approve", ""); code != 409 ||
		!strings.Contains(out["error"].(string), "expired") {
		t.Errorf("expired approve = %d %v", code, out)
	}
}

func TestChangeRequestBadInput(t *testing.T) {
	mel, sam, _, _, _, _ := approvalSetup(t)
	for _, c := range []struct {
		method, path, body string
		want               int
		contains           string
	}{
		{"POST", "/api/v1/flags/new-checkout/environments/dev/requests", `{"enabled":true,"rollout_percentage":1}`, 400, "isn't protected"},
		{"POST", "/api/v1/flags/new-checkout/environments/prod/requests", `{"enabled":true}`, 400, "required"},
		{"POST", "/api/v1/flags/new-checkout/environments/prod/requests", `{"enabled":false,"rollout_percentage":100}`, 400, "doesn't change"},
		{"POST", "/api/v1/flags/new-checkout/environments/prod/requests", `{"enabled":true,"rollout_percentage":101}`, 400, "0-100"},
		{"POST", "/api/v1/flags/nope/environments/prod/requests", `{"enabled":true,"rollout_percentage":1}`, 404, ""},
		{"POST", "/api/v1/flags/new-checkout/environments/qa/requests", `{"enabled":true,"rollout_percentage":1}`, 404, ""},
		{"GET", "/api/v1/requests?status=open", "", 400, "status must be"},
		{"GET", "/api/v1/requests/999", "", 404, ""},
		{"GET", "/api/v1/requests/abc", "", 404, ""},
		{"POST", "/api/v1/requests/999/approve", "", 404, ""},
	} {
		code, out := sam.do(c.method, c.path, c.body)
		if code != c.want || (c.contains != "" && !strings.Contains(out["error"].(string), c.contains)) {
			t.Errorf("%s %s %s = %d %v, want %d %q", c.method, c.path, c.body, code, out, c.want, c.contains)
		}
	}
	// A second pending request is a conflict with a clear message.
	sam.mustDo("POST", "/api/v1/flags/new-checkout/environments/prod/requests", `{"enabled":true,"rollout_percentage":1}`, 201)
	if code, out := mel.do("POST", "/api/v1/flags/new-checkout/environments/prod/requests", `{"enabled":true,"rollout_percentage":2}`); code != 409 ||
		!strings.Contains(out["error"].(string), "already pending") {
		t.Errorf("second request = %d %v", code, out)
	}
}

func TestReviewsAreIdempotent(t *testing.T) {
	_, sam, _, _, ana, _ := approvalSetup(t)
	r := sam.mustDo("POST", "/api/v1/flags/new-checkout/environments/prod/requests", `{"enabled":true,"rollout_percentage":25}`, 201)
	first := ana.doIdem("POST", requestPath(r, "/approve"), `{"comment":"ok"}`, "approve-1")
	retry := ana.doIdem("POST", requestPath(r, "/approve"), `{"comment":"ok"}`, "approve-1")
	if first.Code != 200 || retry.Code != 200 || retry.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("first = %d, retry = %d replayed=%q", first.Code, retry.Code, retry.Header().Get("Idempotent-Replayed"))
	}
}
