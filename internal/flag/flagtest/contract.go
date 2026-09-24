package flagtest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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
		if _, err := s.CreateFlag(ctx, "mel", "new-checkout", "Again", "", ""); !errors.Is(err, flag.ErrConflict) {
			t.Errorf("duplicate: got %v, want ErrConflict", err)
		}
		for _, key := range []string{"", "Bad Key", "-leading-dash", "under_score"} {
			if _, err := s.CreateFlag(ctx, "mel", key, "Name", "", ""); !errors.Is(err, flag.ErrInvalid) {
				t.Errorf("key %q: got %v, want ErrInvalid", key, err)
			}
		}
		if _, err := s.CreateFlag(ctx, "mel", "no-name", "", "", ""); !errors.Is(err, flag.ErrInvalid) {
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
		f, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "prod", cfg, "")
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
		f, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "dev", flag.EnvConfig{Enabled: true, RolloutPercentage: 50}, "")
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
			if _, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "dev", cfg, ""); !errors.Is(err, flag.ErrInvalid) {
				t.Errorf("%+v: got %v, want ErrInvalid", cfg, err)
			}
		}
		if _, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "qa", flag.EnvConfig{}, ""); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("unknown env: got %v, want ErrNotFound", err)
		}
		if _, err := s.UpdateEnvironment(ctx, "sam", "nope", "dev", flag.EnvConfig{}, ""); !errors.Is(err, flag.ErrNotFound) {
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
		if _, err := s.UpdateEnvironment(ctx, "mel", "old", "dev", flag.EnvConfig{}, ""); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("update env of archived: got %v", err)
		}
		if _, err := s.EvalConfig(ctx, "old", "dev"); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("eval archived: got %v", err)
		}
		if _, err := s.CreateFlag(ctx, "mel", "old", "Reuse", "", ""); !errors.Is(err, flag.ErrConflict) {
			t.Errorf("reuse archived key: got %v, want ErrConflict", err)
		}
	})

	t.Run("eval config", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		rules := []eval.Rule{{Attribute: eval.AttributeUserID, Values: []string{"user-1"}, Serve: true}}
		if _, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "prod",
			flag.EnvConfig{Enabled: true, RolloutPercentage: 25, Rules: rules}, ""); err != nil {
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

	t.Run("stewards", func(t *testing.T) {
		s := newStore(t)
		f, err := s.CreateFlag(ctx, "mel", "owned", "Owned", "", "mel")
		if err != nil || f.Steward != "mel" {
			t.Fatalf("create with steward = %+v, %v", f, err)
		}
		if f := mustCreate(t, s, "orphan"); f.Steward != "" {
			t.Errorf("create without steward = %q", f.Steward)
		}
		f, err = s.SetSteward(ctx, "mel", "owned", "sam")
		if err != nil || f.Steward != "sam" {
			t.Fatalf("SetSteward = %+v, %v", f, err)
		}
		if got, _ := s.GetFlag(ctx, "owned"); got.Steward != "sam" {
			t.Errorf("GetFlag steward = %q", got.Steward)
		}
		// Renames keep the steward.
		if f, _ := s.UpdateFlag(ctx, "sam", "owned", "Renamed", ""); f.Steward != "sam" {
			t.Errorf("after rename steward = %q", f.Steward)
		}
		flags, _ := s.ListFlags(ctx)
		if len(flags) != 2 || flags[0].Steward != "" || flags[1].Steward != "sam" {
			t.Errorf("ListFlags stewards = %+v", flags)
		}
		if _, err := s.SetSteward(ctx, "mel", "owned", ""); !errors.Is(err, flag.ErrInvalid) {
			t.Errorf("empty steward: got %v", err)
		}
		if _, err := s.SetSteward(ctx, "mel", "nope", "sam"); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("missing flag: got %v", err)
		}
		s.ArchiveFlag(ctx, "mel", "orphan")
		if _, err := s.SetSteward(ctx, "mel", "orphan", "sam"); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("archived flag: got %v", err)
		}

		events, _ := s.ListAuditEvents(ctx, "owned")
		var got []any
		for _, e := range events {
			if e.Action == flag.ActionSteward {
				got = append(got, decode(t, e.Before), decode(t, e.After))
			}
		}
		want := []any{map[string]any{"steward": "mel"}, map[string]any{"steward": "sam"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("steward events = %v, want %v", got, want)
		}
		if a := decode(t, events[0].After).(map[string]any); a["steward"] != "mel" {
			t.Errorf("create event = %v", a)
		}
		// Assigning an unassigned flag records a null "before".
		mustCreate(t, s, "fresh")
		s.SetSteward(ctx, "mel", "fresh", "ana")
		ev, _ := s.ListAuditEvents(ctx, "fresh")
		if b := decode(t, ev[1].Before); !reflect.DeepEqual(b, map[string]any{"steward": nil}) {
			t.Errorf("before = %v, want steward null", b)
		}
	})

	t.Run("create environment backfills every flag", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		mustCreate(t, s, "old")
		if err := s.ArchiveFlag(ctx, "mel", "old"); err != nil {
			t.Fatal(err)
		}
		qa := flag.Environment{Key: "qa", Name: "QA", Protected: true}
		got, err := s.CreateEnvironment(ctx, "mel", qa)
		if err != nil || got != qa {
			t.Fatalf("CreateEnvironment = %+v, %v", got, err)
		}
		f, err := s.GetFlag(ctx, "new-checkout")
		want := flag.EnvConfig{RolloutPercentage: 100, Rules: []eval.Rule{}}
		if err != nil || !reflect.DeepEqual(f.Environments["qa"], want) {
			t.Fatalf("new-checkout qa = %+v, %v", f.Environments["qa"], err)
		}
		if f, _ := s.GetFlag(ctx, "old"); len(f.Environments) != 4 {
			t.Errorf("archived flag has %d environments, want 4", len(f.Environments))
		}
		// Flags created afterwards get it too, and it can be changed and evaluated.
		mustCreate(t, s, "later")
		if _, err := s.UpdateEnvironment(ctx, "mel", "later", "qa", flag.EnvConfig{Enabled: true, RolloutPercentage: 100}, ""); err != nil {
			t.Fatalf("update later/qa: %v", err)
		}
		if cfg, err := s.EvalConfig(ctx, "later", "qa"); err != nil || !cfg.Enabled {
			t.Errorf("EvalConfig(later, qa) = %+v, %v", cfg, err)
		}
		envs, _ := s.ListEnvironments(ctx)
		var keys []string
		for _, e := range envs {
			keys = append(keys, e.Key)
		}
		if !reflect.DeepEqual(keys, []string{"dev", "prod", "qa", "staging"}) {
			t.Errorf("environments = %v", keys)
		}
	})

	t.Run("create environment rejects duplicates and invalid input", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.CreateEnvironment(ctx, "mel", flag.Environment{Key: "prod", Name: "Again"}); !errors.Is(err, flag.ErrConflict) {
			t.Errorf("duplicate: got %v", err)
		}
		for _, e := range []flag.Environment{{Key: "", Name: "x"}, {Key: "QA", Name: "x"}, {Key: "q a", Name: "x"},
			{Key: strings.Repeat("q", 33), Name: "x"}, {Key: "qa", Name: ""}} {
			if _, err := s.CreateEnvironment(ctx, "mel", e); !errors.Is(err, flag.ErrInvalid) {
				t.Errorf("%+v: got %v", e, err)
			}
		}
	})

	t.Run("update environment settings", func(t *testing.T) {
		s := newStore(t)
		got, err := s.UpdateEnvironmentSettings(ctx, "mel", flag.Environment{Key: "staging", Name: "Stage", Protected: true})
		if err != nil || got != (flag.Environment{Key: "staging", Name: "Stage", Protected: true}) {
			t.Fatalf("got %+v, %v", got, err)
		}
		if e, _ := s.GetEnvironment(ctx, "staging"); !e.Protected || e.Name != "Stage" {
			t.Errorf("GetEnvironment = %+v", e)
		}
		if _, err := s.UpdateEnvironmentSettings(ctx, "mel", flag.Environment{Key: "qa", Name: "QA"}); !errors.Is(err, flag.ErrNotFound) {
			t.Errorf("missing: got %v", err)
		}
		if _, err := s.UpdateEnvironmentSettings(ctx, "mel", flag.Environment{Key: "staging"}); !errors.Is(err, flag.ErrInvalid) {
			t.Errorf("blank name: got %v", err)
		}
		events, err := s.ListEnvironmentAuditEvents(ctx, "staging")
		if err != nil || len(events) != 1 || events[0].Action != flag.ActionEnvironmentUpdated || events[0].Actor != "mel" ||
			!reflect.DeepEqual(decode(t, events[0].Before), map[string]any{"key": "staging", "name": "Staging", "protected": false}) ||
			!reflect.DeepEqual(decode(t, events[0].After), map[string]any{"key": "staging", "name": "Stage", "protected": true}) {
			t.Errorf("events = %+v, %v", events, err)
		}
	})

	t.Run("every mutation is audited", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, "new-checkout")
		mustCreate(t, s, "other")
		if _, err := s.UpdateFlag(ctx, "sam", "new-checkout", "Renamed", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateEnvironment(ctx, "ana", "new-checkout", "prod", flag.EnvConfig{Enabled: true, RolloutPercentage: 25}, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.ArchiveFlag(ctx, "mel", "new-checkout"); err != nil {
			t.Fatal(err)
		}
		// Failed mutations must not be audited.
		s.UpdateEnvironment(ctx, "ana", "other", "prod", flag.EnvConfig{RolloutPercentage: 500}, "")

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
		f, err := s.UpdateEnvironment(ctx, "sam", "new-checkout", "dev", flag.EnvConfig{Enabled: true, Rules: rules}, "")
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
	runRequestContract(t, newStore)
}

func mustCreate(t *testing.T, s flag.Store, key string) flag.Flag {
	t.Helper()
	f, err := s.CreateFlag(context.Background(), "mel", key, "New checkout", "desc", "")
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
