package flagtest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Melmonster13/featuresteward/internal/eval"
	"github.com/Melmonster13/featuresteward/internal/flag"
)

// RunContract checks behavior every flag.Store must share. newStore
// must return an empty store with environments dev, staging, and prod,
// with only prod protected.
func RunContract(t *testing.T, newStore func(t *testing.T) flag.Store) {
	ctx := context.Background()

	t.Run("create sets defaults in every environment", func(t *testing.T) {
		s := newStore(t)
		f := mustCreate(t, s, "new-checkout")
		if f.Name != "New checkout" || f.Description != "desc" || f.ArchivedAt != nil || f.CreatedAt.IsZero() {
			t.Fatalf("unexpected flag: %+v", f)
		}
		want := flag.EnvConfig{Enabled: false, RolloutPercentage: 100, Rules: []eval.Rule{}}
		for _, env := range []string{"dev", "staging", "prod"} {
			if got := f.Environments[env]; !reflect.DeepEqual(got, want) {
				t.Errorf("%s = %+v, want %+v", env, got, want)
			}
		}
		if len(f.Environments) != 3 {
			t.Errorf("got %d environments, want 3", len(f.Environments))
		}
	})

	t.Run("create rejects duplicates and invalid input", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		if _, err := s.CreateFlag(ctx, "mel", "new-checkout", "Again", ""); !errors.Is(err, flag.ErrConflict) {
			t.Errorf("duplicate: got %v, want ErrConflict", err)
		}
		for _, key := range []string{"", "Bad Key", "-leading-dash", "under_score"} {
			if _, err := s.CreateFlag(ctx, "mel", key, "Name", ""); !errors.Is(err, flag.ErrInvalid) {
				t.Errorf("key %q: got %v, want ErrInvalid", key, err)
			}
		}
		if _, err := s.CreateFlag(ctx, "mel", "no-name", "", ""); !errors.Is(err, flag.ErrInvalid) {
			t.Errorf("empty name: got %v, want ErrInvalid", err)
		}
	})

	t.Run("get missing flag", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.GetFlag(ctx, "nope"); !errors.Is(err, flag.ErrNotFound) {
			t.Fatalf("got %v, want ErrNotFound", err)
		}
	})

	t.Run("list is sorted and skips archived", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "zebra")
		mustCreate(t, s, "alpha")
		mustCreate(t, s, "gone")
		if err := s.ArchiveFlag(ctx, "mel", "gone"); err != nil {
			t.Fatal(err)
		}
		flags, err := s.ListFlags(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, f := range flags {
			keys = append(keys, f.Key)
			if len(f.Environments) != 3 {
				t.Errorf("%s has %d environments, want 3", f.Key, len(f.Environments))
			}
		}
		if !reflect.DeepEqual(keys, []string{"alpha", "zebra"}) {
			t.Fatalf("keys = %v", keys)
		}
	})

	t.Run("list empty", func(t *testing.T) {
		flags, err := newStore(t).ListFlags(ctx)
		if err != nil || len(flags) != 0 {
			t.Fatalf("got %v, %v", flags, err)
		}
	})

	t.Run("update metadata", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		f, err := s.UpdateFlag(ctx, "sam", "new-checkout", "Renamed", "new desc")
		if err != nil {
			t.Fatal(err)
		}
		if f.Name != "Renamed" || f.Description != "new desc" || len(f.Environments) != 3 {
			t.Fatalf("unexpected flag: %+v", f)
		}
		if _, err := s.UpdateFlag(ctx, "sam", "nope", "X", ""); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("missing: got %v, want ErrNotFound", err)
		}
	})

	t.Run("update environment only touches that environment", func(t *testing.T) {
		s := newStore(t)
		created := mustCreate(t, s, "new-checkout")
		cfg := flag.EnvConfig{Enabled: true, RolloutPercentage: 25, Rules: []eval.Rule{
			{Attribute: eval.AttributeGroup, Values: []string{"beta"}, Serve: true},
		}}
		f, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "prod", cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.Environments["prod"], cfg) {
			t.Errorf("prod = %+v, want %+v", f.Environments["prod"], cfg)
		}
		if !reflect.DeepEqual(f.Environments["dev"], created.Environments["dev"]) {
			t.Errorf("dev changed: %+v", f.Environments["dev"])
		}
		if f.UpdatedAt.Before(created.UpdatedAt) {
			t.Errorf("updated_at went backwards")
		}
		got, err := s.GetFlag(ctx, "new-checkout")
		if err != nil || !reflect.DeepEqual(got.Environments["prod"], cfg) {
			t.Errorf("GetFlag prod = %+v, %v", got.Environments["prod"], err)
		}
	})

	t.Run("update environment with nil rules stores empty rules", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		f, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "dev", flag.EnvConfig{Enabled: true, RolloutPercentage: 50})
		if err != nil {
			t.Fatal(err)
		}
		if r := f.Environments["dev"].Rules; r == nil || len(r) != 0 {
			t.Errorf("rules = %#v, want empty non-nil", r)
		}
	})

	t.Run("update environment rejects bad input", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		bad := []flag.EnvConfig{
			{RolloutPercentage: 101},
			{RolloutPercentage: -1},
			{RolloutPercentage: 10, Rules: []eval.Rule{{Attribute: "country", Values: []string{"NZ"}}}},
			{RolloutPercentage: 10, Rules: []eval.Rule{{Attribute: eval.AttributeUserID}}},
		}
		for _, cfg := range bad {
			if _, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "dev", cfg); !errors.Is(err, flag.ErrInvalid) {
				t.Errorf("%+v: got %v, want ErrInvalid", cfg, err)
			}
		}
		if _, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "qa", flag.EnvConfig{}); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("unknown env: got %v, want ErrNotFound", err)
		}
		if _, err := s.UpdateEnvironment(ctx, "sam", "nope", "dev", flag.EnvConfig{}); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("missing flag: got %v, want ErrNotFound", err)
		}
	})

	t.Run("archived flags are read-only and not evaluated", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "old")
		if err := s.ArchiveFlag(ctx, "mel", "old"); err != nil {
			t.Fatal(err)
		}
		f, err := s.GetFlag(ctx, "old")
		if err != nil || f.ArchivedAt == nil {
			t.Fatalf("GetFlag = %+v, %v; want archived flag", f, err)
		}
		if err := s.ArchiveFlag(ctx, "mel", "old"); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("archive twice: got %v", err)
		}
		if _, err := s.UpdateFlag(ctx, "mel", "old", "X", ""); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("update archived: got %v", err)
		}
		if _, err := s.UpdateEnvironment(ctx, "mel", "old", "dev", flag.EnvConfig{}); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("update env of archived: got %v", err)
		}
		if _, err := s.EvalConfig(ctx, "old", "dev"); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("eval archived: got %v", err)
		}
		if _, err := s.CreateFlag(ctx, "mel", "old", "Reuse", ""); !errors.Is(err, flag.ErrConflict) {
			t.Errorf("reuse archived key: got %v, want ErrConflict", err)
		}
	})

	t.Run("eval config", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		rules := []eval.Rule{{Attribute: eval.AttributeUserID, Values: []string{"user-1"}, Serve: true}}
		if _, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "prod",
			flag.EnvConfig{Enabled: true, RolloutPercentage: 25, Rules: rules}); err != nil {
			t.Fatal(err)
		}
		got, err := s.EvalConfig(ctx, "new-checkout", "prod")
		want := eval.Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 25, Rules: rules}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, %v; want %+v", got, err, want)
		}
		if _, err := s.EvalConfig(ctx, "new-checkout", "qa"); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("unknown env: got %v", err)
		}
		if _, err := s.EvalConfig(ctx, "nope", "prod"); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("missing flag: got %v", err)
		}
	})

	t.Run("environments", func(t *testing.T) {
		s := newStore(t)
		envs, err := s.ListEnvironments(ctx)
		want := []flag.Environment{
			{Key: "dev", Name: "Development"},
			{Key: "prod", Name: "Production", Protected: true},
			{Key: "staging", Name: "Staging"},
		}
		if err != nil || !reflect.DeepEqual(envs, want) {
			t.Fatalf("got %+v, %v", envs, err)
		}
		if e, err := s.GetEnvironment(ctx, "prod"); err != nil || e != want[1] {
			t.Errorf("GetEnvironment(prod) = %+v, %v", e, err)
		}
		if _, err := s.GetEnvironment(ctx, "qa"); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("unknown env: got %v", err)
		}
	})

	t.Run("every mutation is audited", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		mustCreate(t, s, "other")
		if _, err := s.UpdateFlag(ctx, "sam", "new-checkout", "Renamed", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateEnvironment(ctx, "ana", "new-checkout", "prod", flag.EnvConfig{Enabled: true, RolloutPercentage: 25}); err != nil {
			t.Fatal(err)
		}
		if err := s.ArchiveFlag(ctx, "mel", "new-checkout"); err != nil {
			t.Fatal(err)
		}
		// Failed mutations must not be audited.
		s.UpdateEnvironment(ctx, "ana", "other", "prod", flag.EnvConfig{RolloutPercentage: 500})

		events, err := s.ListAuditEvents(ctx, "new-checkout")
		if err != nil {
			t.Fatal(err)
		}
		type ev struct {
			Actor, Action, Env string
			Before, After      any
		}
		want := []ev{
			{"mel", flag.ActionCreated, "", nil, map[string]any{"key": "new-checkout", "name": "New checkout", "description": "desc"}},
			{"sam", flag.ActionUpdated, "",
				map[string]any{"key": "new-checkout", "name": "New checkout", "description": "desc"},
				map[string]any{"key": "new-checkout", "name": "Renamed", "description": ""}},
			{"ana", flag.ActionEnvUpdated, "prod",
				map[string]any{"enabled": false, "rollout_percentage": 100.0, "rules": []any{}},
				map[string]any{"enabled": true, "rollout_percentage": 25.0, "rules": []any{}}},
			{"mel", flag.ActionArchived, "", nil, nil},
		}
		if len(events) != len(want) {
			t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
		}
		for i, e := range events {
			got := ev{e.Actor, e.Action, e.Environment, decode(t, e.Before), decode(t, e.After)}
			if !reflect.DeepEqual(got, want[i]) {
				t.Errorf("event %d = %+v\nwant %+v", i, got, want[i])
			}
			if e.FlagKey != "new-checkout" || e.OccurredAt.IsZero() {
				t.Errorf("event %d: key=%q occurred_at=%v", i, e.FlagKey, e.OccurredAt)
			}
			if i > 0 && e.ID <= events[i-1].ID {
				t.Errorf("event %d out of order", i)
			}
		}
		other, _ := s.ListAuditEvents(ctx, "other")
		if len(other) != 1 {
			t.Errorf("other flag has %d events, want 1 (create only)", len(other))
		}
	})

	t.Run("returned flags don't alias store state", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		rules := []eval.Rule{{Attribute: eval.AttributeGroup, Values: []string{"beta"}, Serve: true}}
		f, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "dev", flag.EnvConfig{Enabled: true, Rules: rules})
		if err != nil {
			t.Fatal(err)
		}
		rules[0].Values[0] = "mutated"
		f.Environments["dev"].Rules[0].Values[0] = "mutated"
		got, _ := s.EvalConfig(ctx, "new-checkout", "dev")
		if got.Rules[0].Values[0] != "beta" {
			t.Fatalf("store state changed via caller's slice: %+v", got.Rules)
		}
	})
}

func mustCreate(t *testing.T, s flag.Store, key string) flag.Flag {
	t.Helper()
	f, err := s.CreateFlag(context.Background(), "mel", key, "New checkout", "desc")
	if err != nil {
		t.Fatalf("CreateFlag(%q): %v", key, err)
	}
	return f
}

func decode(t *testing.T, b []byte) any {
	t.Helper()
	if b == nil {
		return nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("bad audit JSON %s: %v", b, err)
	}
	return v
}
