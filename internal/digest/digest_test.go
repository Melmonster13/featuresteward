package digest

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
	"time"

	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
)

var ctx = context.Background()

type claimer struct{ claimed, released int }

func (c *claimer) ReleaseJob(_ context.Context, name string) error {
	c.released++
	c.claimed = 0
	return nil
}

func (c *claimer) ClaimJob(_ context.Context, name string, every time.Duration) (bool, error) {
	if name != JobName || every != 23*time.Hour {
		return false, errors.New("unexpected job")
	}
	c.claimed++
	return c.claimed == 1, nil // only the first claim wins
}

// setup has flags that become stale 31 days after now: old-banner
// (sam's, unused), launched (sam's, always on), orphan (no steward,
// unused), and busy (partial rollout, evaluated recently: not stale).
func setup(t *testing.T) (*Digest, *flagtest.Memory, *[]string, *time.Time) {
	flags := flagtest.NewMemory()
	for _, f := range [][2]string{{"old-banner", "sam"}, {"launched", "sam"}, {"orphan", ""}, {"busy", "kim"}} {
		if _, err := flags.CreateFlag(ctx, "mel", f[0], "<!channel> "+f[0], "", f[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		flags.UpdateEnvironment(ctx, "mel", "launched", env, flag.EnvConfig{Enabled: true, RolloutPercentage: 100}, "x")
	}
	flags.UpdateEnvironment(ctx, "mel", "busy", "prod", flag.EnvConfig{Enabled: true, RolloutPercentage: 30}, "x")
	now := time.Now().Add(31 * 24 * time.Hour)
	flags.RecordEvaluations(ctx, []flag.Evaluation{
		{Flag: "launched", Environment: "prod", At: now.Add(-time.Hour)},
		{Flag: "busy", Environment: "prod", At: now.Add(-time.Hour)},
	})
	var sent []string
	d := &Digest{
		Flags: flags, Claim: &claimer{}, After: 30 * 24 * time.Hour, Every: 23 * time.Hour,
		Send: func(_ context.Context, text string) error { sent = append(sent, text); return nil },
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:  func() time.Time { return now },
	}
	return d, flags, &sent, &now
}

func TestDigestGroupsByStewardOnce(t *testing.T) {
	d, flags, sent, now := setup(t)
	if err := d.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d digests", len(*sent))
	}
	text := (*sent)[0]
	for _, want := range []string{
		"*FeatureSteward: 3 stale flags*",
		"*No steward*\n• `orphan` — unused since",
		"*@sam*\n• `launched` — on for everyone since",
		"• `old-banner` — unused since",
		"keep the new code",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("digest missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "busy") || strings.Contains(text, "<!channel>") {
		t.Errorf("digest has a fresh flag or a flag name:\n%s", text)
	}
	if f, _ := flags.GetFlag(ctx, "orphan"); f.StaleNotifiedAt == nil || !f.StaleNotifiedAt.Equal(*now) {
		t.Errorf("orphan notified at %v", f.StaleNotifiedAt)
	}
	// Another server (or the next hourly check) doesn't send it again.
	d.Run(ctx)
	if len(*sent) != 1 {
		t.Errorf("sent %d digests after a second run", len(*sent))
	}
}

func TestDigestRemindsWeekly(t *testing.T) {
	d, flags, _, now := setup(t)
	flags.MarkStaleNotified(ctx, []string{"old-banner", "launched", "orphan"}, now.Add(-6*24*time.Hour))
	all, _ := flags.ListFlags(ctx)
	envs, _ := flags.ListEnvironments(ctx)
	if text, keys := d.Build(all, envs, nil, *now); len(keys) != 0 {
		t.Errorf("reminded within a week: %v\n%s", keys, text)
	}
	flags.MarkStaleNotified(ctx, []string{"orphan"}, now.Add(-8*24*time.Hour))
	all, _ = flags.ListFlags(ctx)
	if _, keys := d.Build(all, envs, nil, *now); len(keys) != 1 || keys[0] != "orphan" {
		t.Errorf("after a week = %v", keys)
	}
}

func TestDigestLinksAndPendingRequests(t *testing.T) {
	d, flags, _, now := setup(t)
	d.Public = "https://flags.example.com/"
	all, _ := flags.ListFlags(ctx)
	envs, _ := flags.ListEnvironments(ctx)
	text, keys := d.Build(all, envs, map[string]bool{"launched": true}, *now)
	if !strings.Contains(text, "<https://flags.example.com/#/flags/orphan|orphan>") || strings.Contains(text, "launched") || len(keys) != 2 {
		t.Errorf("digest = %v\n%s", keys, text)
	}
}

func TestFailedSendsAreRetried(t *testing.T) {
	d, flags, _, _ := setup(t)
	d.Send = func(context.Context, string) error { return errors.New("webhook down") }
	if err := d.Run(ctx); err == nil {
		t.Fatal("no error")
	}
	if f, _ := flags.GetFlag(ctx, "orphan"); f.StaleNotifiedAt != nil {
		t.Error("marked notified although sending failed")
	}
	// The claim is given back, so the next check tries again.
	var sent []string
	d.Send = func(_ context.Context, text string) error { sent = append(sent, text); return nil }
	if err := d.Run(ctx); err != nil || len(sent) != 1 {
		t.Errorf("retry: %v, sent %d", err, len(sent))
	}
}

func TestWebhook(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.URL.Path != "/services/SECRET-PATH" {
			w.WriteHeader(400)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	w, err := NewWebhook(srv.URL + "/services/SECRET-PATH")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Send(ctx, "hello"); err != nil || got["text"] != "hello" {
		t.Fatalf("send = %v, got %v", err, got)
	}

	w, _ = NewWebhook(srv.URL + "/services/WRONG-SECRET")
	if err := w.Send(ctx, "x"); err == nil || !strings.Contains(err.Error(), "HTTP 400") || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("rejected = %v", err)
	}
	srv.Close()
	w, _ = NewWebhook(srv.URL + "/services/SECRET-PATH")
	if err := w.Send(ctx, "x"); err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), srv.URL) {
		t.Errorf("unreachable error leaks the URL: %v", err)
	}
}

func TestWebhookURLs(t *testing.T) {
	for _, ok := range []string{"https://hooks.slack.com/services/x", "http://localhost:9000/hook", "http://127.0.0.1/h"} {
		if _, err := NewWebhook(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://hooks.example.com/secret", "ftp://x", "not a url", "https://"} {
		if _, err := NewWebhook(bad); err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
