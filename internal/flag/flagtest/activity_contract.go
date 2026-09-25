package flagtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Melmonster13/featuresteward/internal/errs"
	"github.com/Melmonster13/featuresteward/internal/flag"
)

func runActivityContract(t *testing.T, newStore func(t *testing.T) flag.Store) {
	ctx := context.Background()
	// Postgres and this machine's clocks can differ a little.
	near := func(a, b time.Time) bool { return a.Sub(b).Abs() < 5*time.Second }

	t.Run("activity starts at creation", func(t *testing.T) {
		s := newStore(t)
		f := mustCreate(t, s, "new-checkout")
		if len(f.Activity) != 3 {
			t.Fatalf("activity = %v", f.Activity)
		}
		for env, a := range f.Activity {
			if !near(a.ChangedAt, time.Now()) || !near(a.EvaluatedAt, time.Now()) {
				t.Errorf("%s activity = %+v", env, a)
			}
		}
		// Creating an environment starts activity there too.
		s.CreateEnvironment(ctx, "mel", flag.Environment{Key: "qa", Name: "QA"})
		f, _ = s.GetFlag(ctx, "new-checkout")
		if a := f.Activity["qa"]; !near(a.ChangedAt, time.Now()) || !near(a.EvaluatedAt, time.Now()) {
			t.Errorf("qa activity = %+v", a)
		}
	})

	t.Run("changes update changed-at in that environment only", func(t *testing.T) {
		s := newStore(t)
		created := mustCreate(t, s, "new-checkout")
		old := time.Now().Add(-48 * time.Hour)
		s.RecordEvaluations(ctx, []flag.Evaluation{{Flag: "new-checkout", Environment: "prod", At: time.Now()}})
		time.Sleep(10 * time.Millisecond)
		f, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "prod", flag.EnvConfig{Enabled: true, RolloutPercentage: 5}, "")
		if err != nil {
			t.Fatal(err)
		}
		if !f.Activity["prod"].ChangedAt.After(created.Activity["prod"].ChangedAt) {
			t.Errorf("prod changed-at didn't move: %+v", f.Activity["prod"])
		}
		if !f.Activity["dev"].ChangedAt.Equal(created.Activity["dev"].ChangedAt) {
			t.Errorf("dev changed-at moved: %+v", f.Activity["dev"])
		}
		if f.Activity["prod"].EvaluatedAt.Before(old) {
			t.Errorf("a change reset evaluated-at: %+v", f.Activity["prod"])
		}
		// Approvals count as changes.
		r, err := s.CreateChangeRequest(ctx, "sam", "new-checkout", "dev", flag.EnvConfig{Enabled: true, RolloutPercentage: 100}, "", time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
		s.ApproveChangeRequest(ctx, "ana", r.ID, "")
		f, _ = s.GetFlag(ctx, "new-checkout")
		if !f.Activity["dev"].ChangedAt.After(created.Activity["dev"].ChangedAt) {
			t.Errorf("approval didn't update changed-at: %+v", f.Activity["dev"])
		}
	})

	t.Run("record evaluations keeps the latest", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		later := time.Now().Add(time.Hour).Truncate(time.Microsecond)
		err := s.RecordEvaluations(ctx, []flag.Evaluation{
			{Flag: "new-checkout", Environment: "prod", At: later},
			{Flag: "new-checkout", Environment: "dev", At: time.Now().Add(-time.Hour)}, // older: ignored
			{Flag: "nope", Environment: "prod", At: later},                             // unknown: ignored
			{Flag: "new-checkout", Environment: "qa", At: later},                       // unknown: ignored
		})
		if err != nil {
			t.Fatal(err)
		}
		f, _ := s.GetFlag(ctx, "new-checkout")
		if !f.Activity["prod"].EvaluatedAt.Equal(later) {
			t.Errorf("prod evaluated-at = %v, want %v", f.Activity["prod"].EvaluatedAt, later)
		}
		if !near(f.Activity["dev"].EvaluatedAt, time.Now()) {
			t.Errorf("an older time replaced dev's: %v", f.Activity["dev"].EvaluatedAt)
		}
		if err := s.RecordEvaluations(ctx, nil); err != nil {
			t.Errorf("empty batch: %v", err)
		}
		// Recording isn't a change.
		events, _ := s.ListAuditEvents(ctx, "new-checkout")
		if len(events) != 1 {
			t.Errorf("recording evaluations was audited: %d events", len(events))
		}
	})

	t.Run("permanent flags", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "kill-switch")
		f, err := s.SetPermanent(ctx, "kim", "kill-switch", "  ops kill switch for payments  ")
		if err != nil || f.PermanentReason != "ops kill switch for payments" {
			t.Fatalf("SetPermanent = %q, %v", f.PermanentReason, err)
		}
		if got, _ := s.GetFlag(ctx, "kill-switch"); got.PermanentReason != "ops kill switch for payments" {
			t.Errorf("stored reason = %q", got.PermanentReason)
		}
		if f, err = s.SetPermanent(ctx, "mel", "kill-switch", ""); err != nil || f.PermanentReason != "" {
			t.Errorf("clear = %q, %v", f.PermanentReason, err)
		}
		events, _ := s.ListAuditEvents(ctx, "kill-switch")
		set, cleared := events[1], events[2]
		if set.Action != flag.ActionPermanent || set.Actor != "kim" || decode(t, set.Before).(map[string]any)["permanent_reason"] != nil ||
			decode(t, set.After).(map[string]any)["permanent_reason"] != "ops kill switch for payments" {
			t.Errorf("set event = %+v", set)
		}
		if cleared.Actor != "mel" || decode(t, cleared.After).(map[string]any)["permanent_reason"] != nil {
			t.Errorf("clear event = %+v", cleared)
		}
		if _, err := s.SetPermanent(ctx, "mel", "kill-switch", strings.Repeat("x", 501)); !errors.Is(err, errs.ErrInvalid) {
			t.Errorf("long reason: %v", err)
		}
		if _, err := s.SetPermanent(ctx, "mel", "nope", "x"); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("unknown flag: %v", err)
		}
		s.ArchiveFlag(ctx, "mel", "kill-switch")
		if _, err := s.SetPermanent(ctx, "mel", "kill-switch", "x"); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("archived flag: %v", err)
		}
	})
}
