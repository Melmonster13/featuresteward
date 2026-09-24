package flagtest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Melmonster13/featuresteward/internal/audit"
	"github.com/Melmonster13/featuresteward/internal/errs"
	"github.com/Melmonster13/featuresteward/internal/eval"
	"github.com/Melmonster13/featuresteward/internal/flag"
)

func runRequestContract(t *testing.T, newStore func(t *testing.T) flag.Store) {
	ctx := context.Background()
	week := func() time.Time { return time.Now().Add(flag.RequestTTL) }
	proposed := flag.EnvConfig{Enabled: true, RolloutPercentage: 25,
		Rules: []eval.Rule{{Attribute: eval.AttributeGroup, Values: []string{"staff"}, Serve: true}}}
	request := func(t *testing.T, s flag.Store, actor string, cfg flag.EnvConfig, expires time.Time) flag.ChangeRequest {
		t.Helper()
		r, err := s.CreateChangeRequest(ctx, actor, "new-checkout", "prod", cfg, "launch to staff", expires)
		if err != nil {
			t.Fatalf("CreateChangeRequest: %v", err)
		}
		return r
	}

	t.Run("approve applies the proposed config", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		r := request(t, s, "sam", proposed, week())
		if r.ID == 0 || r.FlagKey != "new-checkout" || r.Environment != "prod" || r.RequestedBy != "sam" ||
			r.Reason != "launch to staff" || r.Status != flag.RequestPending || r.ResolvedAt != nil || r.CreatedAt.IsZero() ||
			!r.Base.Equal(flag.EnvConfig{RolloutPercentage: 100}) || !r.Proposed.Equal(proposed) {
			t.Fatalf("created = %+v", r)
		}
		if got, err := s.GetChangeRequest(ctx, r.ID); err != nil || got.Status != flag.RequestPending || !got.Proposed.Equal(proposed) {
			t.Fatalf("GetChangeRequest = %+v, %v", got, err)
		}
		// Requesting doesn't change the flag.
		if f, _ := s.GetFlag(ctx, "new-checkout"); f.Environments["prod"].Enabled {
			t.Fatal("a pending request changed prod")
		}

		got, err := s.ApproveChangeRequest(ctx, "kim", r.ID, "looks good")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != flag.RequestApproved || got.ReviewedBy != "kim" || got.ReviewComment != "looks good" || got.ResolvedAt == nil {
			t.Errorf("approved = %+v", got)
		}
		f, _ := s.GetFlag(ctx, "new-checkout")
		if !f.Environments["prod"].Equal(proposed) {
			t.Errorf("prod = %+v, want %+v", f.Environments["prod"], proposed)
		}

		events, _ := s.ListAuditEvents(ctx, "new-checkout")
		var actions []string
		for _, e := range events[1:] { // after flag.created
			actions = append(actions, e.Actor+" "+e.Action)
		}
		want := []string{"sam change_request.created", "kim flag.environment_updated", "kim change_request.approved"}
		if !equal(actions, want) {
			t.Fatalf("audit = %v, want %v", actions, want)
		}
		applied := events[2]
		if applied.Environment != "prod" || decode(t, applied.After).(map[string]any)["rollout_percentage"] != float64(25) {
			t.Errorf("applied event = %+v", applied)
		}
		approved := decode(t, events[3].After).(map[string]any)
		if approved["id"] != float64(r.ID) || approved["status"] != "approved" || approved["comment"] != "looks good" ||
			approved["requested_by"] != "sam" {
			t.Errorf("approved snapshot = %v", approved)
		}
	})

	t.Run("nobody reviews their own request", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		r := request(t, s, "sam", proposed, week())
		if _, err := s.ApproveChangeRequest(ctx, "sam", r.ID, ""); !errors.Is(err, errs.ErrForbidden) {
			t.Errorf("self-approve: got %v", err)
		}
		if _, err := s.RejectChangeRequest(ctx, "sam", r.ID, ""); !errors.Is(err, errs.ErrForbidden) {
			t.Errorf("self-reject: got %v", err)
		}
		if _, err := s.CancelChangeRequest(ctx, "kim", r.ID); !errors.Is(err, errs.ErrForbidden) {
			t.Errorf("cancel by someone else: got %v", err)
		}
		if got, _ := s.GetChangeRequest(ctx, r.ID); got.Status != flag.RequestPending {
			t.Errorf("refused actions changed status to %q", got.Status)
		}
	})

	t.Run("reject and cancel leave the flag alone", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		r := request(t, s, "sam", proposed, week())
		got, err := s.RejectChangeRequest(ctx, "kim", r.ID, "not during the sale")
		if err != nil || got.Status != flag.RequestRejected || got.ReviewedBy != "kim" || got.ReviewComment != "not during the sale" || got.ResolvedAt == nil {
			t.Fatalf("reject = %+v, %v", got, err)
		}
		r2 := request(t, s, "sam", proposed, week())
		got, err = s.CancelChangeRequest(ctx, "sam", r2.ID)
		if err != nil || got.Status != flag.RequestCancelled || got.ResolvedAt == nil {
			t.Fatalf("cancel = %+v, %v", got, err)
		}
		if f, _ := s.GetFlag(ctx, "new-checkout"); f.Environments["prod"].Enabled {
			t.Error("reject or cancel changed prod")
		}
		// Closed requests can't be reviewed again.
		for name, fn := range map[string]func() error{
			"approve rejected":  func() error { _, err := s.ApproveChangeRequest(ctx, "kim", r.ID, ""); return err },
			"approve cancelled": func() error { _, err := s.ApproveChangeRequest(ctx, "kim", r2.ID, ""); return err },
			"reject cancelled":  func() error { _, err := s.RejectChangeRequest(ctx, "kim", r2.ID, ""); return err },
			"cancel rejected":   func() error { _, err := s.CancelChangeRequest(ctx, "sam", r.ID); return err },
		} {
			if err := fn(); !errors.Is(err, errs.ErrConflict) {
				t.Errorf("%s: got %v", name, err)
			}
		}
		events, _ := s.ListAuditEvents(ctx, "new-checkout")
		last := events[len(events)-1]
		if last.Action != flag.ActionRequestCancelled || last.Actor != "sam" {
			t.Errorf("last event = %+v", last)
		}
	})

	t.Run("one pending request per flag and environment", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		r := request(t, s, "sam", proposed, week())
		if _, err := s.CreateChangeRequest(ctx, "kim", "new-checkout", "prod", proposed, "", week()); !errors.Is(err, errs.ErrConflict) {
			t.Errorf("second pending: got %v", err)
		}
		if _, err := s.CreateChangeRequest(ctx, "kim", "new-checkout", "staging", proposed, "", week()); err != nil {
			t.Errorf("other environment: %v", err)
		}
		s.CancelChangeRequest(ctx, "sam", r.ID)
		if _, err := s.CreateChangeRequest(ctx, "kim", "new-checkout", "prod", proposed, "", week()); err != nil {
			t.Errorf("after cancel: %v", err)
		}
	})

	t.Run("requests are validated", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		mustCreate(t, s, "old")
		s.ArchiveFlag(ctx, "mel", "old")
		current := flag.EnvConfig{RolloutPercentage: 100}
		for name, c := range map[string]struct {
			key, env string
			cfg      flag.EnvConfig
			want     error
		}{
			"no change":      {"new-checkout", "prod", current, errs.ErrInvalid},
			"bad percentage": {"new-checkout", "prod", flag.EnvConfig{RolloutPercentage: 101}, errs.ErrInvalid},
			"empty rule":     {"new-checkout", "prod", flag.EnvConfig{Rules: []eval.Rule{{Attribute: "group"}}}, errs.ErrInvalid},
			"unknown flag":   {"nope", "prod", proposed, errs.ErrNotFound},
			"unknown env":    {"new-checkout", "qa", proposed, errs.ErrNotFound},
			"archived flag":  {"old", "prod", proposed, errs.ErrNotFound},
		} {
			if _, err := s.CreateChangeRequest(ctx, "sam", c.key, c.env, c.cfg, "", week()); !errors.Is(err, c.want) {
				t.Errorf("%s: got %v, want %v", name, err, c.want)
			}
		}
		for name, fn := range map[string]func() error{
			"get":     func() error { _, err := s.GetChangeRequest(ctx, 999); return err },
			"approve": func() error { _, err := s.ApproveChangeRequest(ctx, "kim", 999, ""); return err },
			"reject":  func() error { _, err := s.RejectChangeRequest(ctx, "kim", 999, ""); return err },
			"cancel":  func() error { _, err := s.CancelChangeRequest(ctx, "kim", 999); return err },
		} {
			if err := fn(); !errors.Is(err, errs.ErrNotFound) {
				t.Errorf("%s unknown id: got %v", name, err)
			}
		}
	})

	t.Run("approval fails if the environment changed", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		r := request(t, s, "sam", proposed, week())
		killed := flag.EnvConfig{Enabled: false, RolloutPercentage: 0}
		if _, err := s.UpdateEnvironment(ctx, "mel", "new-checkout", "prod", killed, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApproveChangeRequest(ctx, "kim", r.ID, ""); !errors.Is(err, errs.ErrConflict) {
			t.Fatalf("stale approve: got %v", err)
		}
		if f, _ := s.GetFlag(ctx, "new-checkout"); !f.Environments["prod"].Equal(killed) {
			t.Errorf("stale approve changed prod to %+v", f.Environments["prod"])
		}
		if got, _ := s.GetChangeRequest(ctx, r.ID); got.Status != flag.RequestPending {
			t.Errorf("status = %q; a failed approval shouldn't close the request", got.Status)
		}
		// Archiving the flag also blocks approval.
		mustCreate(t, s, "dark-mode")
		r2, _ := s.CreateChangeRequest(ctx, "sam", "dark-mode", "prod", proposed, "", week())
		s.ArchiveFlag(ctx, "mel", "dark-mode")
		if _, err := s.ApproveChangeRequest(ctx, "kim", r2.ID, ""); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("approve for archived flag: got %v", err)
		}
	})

	t.Run("requests expire", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		mustCreate(t, s, "dark-mode")
		old := request(t, s, "sam", proposed, time.Now().Add(-time.Second))
		if _, err := s.ApproveChangeRequest(ctx, "kim", old.ID, ""); !errors.Is(err, errs.ErrConflict) {
			t.Errorf("approve expired: got %v", err)
		}
		// A new request replaces an expired one without waiting for cleanup.
		fresh := request(t, s, "kim", proposed, week())
		if got, _ := s.GetChangeRequest(ctx, old.ID); got.Status != flag.RequestExpired || got.ResolvedAt == nil || got.ReviewedBy != "" {
			t.Errorf("replaced request = %+v", got)
		}
		stale, _ := s.CreateChangeRequest(ctx, "sam", "dark-mode", "prod", proposed, "", time.Now().Add(-time.Second))

		n, err := s.ExpireChangeRequests(ctx)
		if err != nil || n != 1 {
			t.Fatalf("ExpireChangeRequests = %d, %v; want 1", n, err)
		}
		if got, _ := s.GetChangeRequest(ctx, stale.ID); got.Status != flag.RequestExpired {
			t.Errorf("stale status = %q", got.Status)
		}
		if got, _ := s.GetChangeRequest(ctx, fresh.ID); got.Status != flag.RequestPending {
			t.Errorf("cleanup expired a live request: %q", got.Status)
		}
		events, _ := s.ListAuditEvents(ctx, "dark-mode")
		last := events[len(events)-1]
		if last.Action != flag.ActionRequestExpired || last.Actor != audit.SystemActor {
			t.Errorf("expiry event = %+v", last)
		}
		if n, _ := s.ExpireChangeRequests(ctx); n != 0 {
			t.Errorf("second cleanup expired %d", n)
		}
	})

	t.Run("list filters, newest first", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		mustCreate(t, s, "dark-mode")
		a := request(t, s, "sam", proposed, week())
		b, _ := s.CreateChangeRequest(ctx, "sam", "dark-mode", "prod", proposed, "", week())
		c, _ := s.CreateChangeRequest(ctx, "sam", "dark-mode", "staging", proposed, "", week())
		s.RejectChangeRequest(ctx, "kim", b.ID, "")

		ids := func(f flag.RequestFilter) []int64 {
			t.Helper()
			rs, err := s.ListChangeRequests(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			out := []int64{}
			for _, r := range rs {
				out = append(out, r.ID)
			}
			return out
		}
		if got := ids(flag.RequestFilter{}); !equal(got, []int64{c.ID, b.ID, a.ID}) {
			t.Errorf("all = %v", got)
		}
		if got := ids(flag.RequestFilter{Status: flag.RequestPending}); !equal(got, []int64{c.ID, a.ID}) {
			t.Errorf("pending = %v", got)
		}
		if got := ids(flag.RequestFilter{FlagKey: "dark-mode"}); !equal(got, []int64{c.ID, b.ID}) {
			t.Errorf("dark-mode = %v", got)
		}
		if got := ids(flag.RequestFilter{Status: flag.RequestRejected, FlagKey: "dark-mode"}); !equal(got, []int64{b.ID}) {
			t.Errorf("rejected dark-mode = %v", got)
		}
		if got := ids(flag.RequestFilter{FlagKey: "nope"}); len(got) != 0 {
			t.Errorf("unknown flag = %v", got)
		}
	})

	t.Run("emergency changes record their reason", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		if _, err := s.UpdateEnvironment(ctx, "mel", "new-checkout", "prod", proposed, "checkout is down"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateEnvironment(ctx, "mel", "new-checkout", "dev", proposed, ""); err != nil {
			t.Fatal(err)
		}
		events, _ := s.ListAuditEvents(ctx, "new-checkout")
		emergency := decode(t, events[1].After).(map[string]any)
		if emergency["emergency_reason"] != "checkout is down" || emergency["rollout_percentage"] != float64(25) || emergency["enabled"] != true {
			t.Errorf("emergency event after = %v", emergency)
		}
		if normal := decode(t, events[2].After).(map[string]any); normal["emergency_reason"] != nil {
			t.Errorf("normal change has a reason: %v", normal)
		}
	})

	t.Run("concurrent approvals apply once", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		r := request(t, s, "sam", proposed, week())
		errc := make(chan error, 8)
		for i := range 8 {
			go func() {
				_, err := s.ApproveChangeRequest(ctx, fmt.Sprintf("approver%d", i), r.ID, "")
				errc <- err
			}()
		}
		ok := 0
		for range 8 {
			err := <-errc
			switch {
			case err == nil:
				ok++
			case !errors.Is(err, errs.ErrConflict):
				t.Errorf("unexpected error: %v", err)
			}
		}
		if ok != 1 {
			t.Fatalf("%d approvals succeeded, want 1", ok)
		}
		events, _ := s.ListAuditEvents(ctx, "new-checkout")
		applied := 0
		for _, e := range events {
			if e.Action == flag.ActionEnvUpdated {
				applied++
			}
		}
		if applied != 1 {
			t.Errorf("config applied %d times", applied)
		}
	})

	t.Run("returned requests don't alias store state", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		cfg := flag.EnvConfig{Enabled: true, RolloutPercentage: 10,
			Rules: []eval.Rule{{Attribute: eval.AttributeGroup, Values: []string{"beta"}, Serve: true}}}
		r := request(t, s, "sam", cfg, week())
		cfg.Rules[0].Values[0] = "changed"
		r.Proposed.Rules[0].Values[0] = "changed"
		got, _ := s.GetChangeRequest(ctx, r.ID)
		if got.Proposed.Rules[0].Values[0] != "beta" {
			t.Errorf("store state changed via a caller's slice: %+v", got.Proposed.Rules)
		}
	})
}

func equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
