package flag

import (
	"testing"
	"time"

	"github.com/Melmonster13/featuresteward/internal/eval"
)

var (
	now     = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	after   = 30 * 24 * time.Hour
	longAgo = now.Add(-40 * 24 * time.Hour)
	recent  = now.Add(-2 * 24 * time.Hour)
	envs    = []Environment{{Key: "dev"}, {Key: "prod", Protected: true}, {Key: "staging"}}
	off     = EnvConfig{RolloutPercentage: 100}
	on      = EnvConfig{Enabled: true, RolloutPercentage: 100}
	partial = EnvConfig{Enabled: true, RolloutPercentage: 25}
	onRule  = eval.Rule{Attribute: eval.AttributeGroup, Values: []string{"staff"}, Serve: true}
	offRule = eval.Rule{Attribute: eval.AttributeGroup, Values: []string{"banned"}, Serve: false}
)

// flagWith has the given config everywhere, changed and evaluated at the
// given times, unless overridden per environment.
func flagWith(cfg EnvConfig, changed, evaluated time.Time) Flag {
	f := Flag{Key: "x", Environments: map[string]EnvConfig{}, Activity: map[string]Activity{}}
	for _, e := range envs {
		f.Environments[e.Key] = cfg
		f.Activity[e.Key] = Activity{ChangedAt: changed, EvaluatedAt: evaluated}
	}
	return f
}

func TestAssess(t *testing.T) {
	mixed := flagWith(off, longAgo, recent) // off everywhere but dev
	mixed.Environments["dev"] = on
	devOnly := flagWith(off, longAgo, recent)
	devOnly.Activity["dev"] = Activity{ChangedAt: recent, EvaluatedAt: recent}
	prodOnDevOff := flagWith(off, longAgo, recent)
	prodOnDevOff.Environments["prod"] = on
	permanent := flagWith(on, longAgo, recent)
	permanent.PermanentReason = "ops kill switch"
	archivedAt := longAgo
	archived := flagWith(on, longAgo, longAgo)
	archived.ArchivedAt = &archivedAt
	usedInProd := flagWith(on, longAgo, longAgo)
	usedInProd.Activity["prod"] = Activity{ChangedAt: longAgo, EvaluatedAt: recent}

	for name, c := range map[string]struct {
		f       Flag
		pending bool
		want    StaleReason
	}{
		"fresh":                          {flagWith(on, recent, recent), false, ""},
		"on everywhere, settled":         {flagWith(on, longAgo, recent), false, StaleOn},
		"off everywhere, settled":        {flagWith(off, longAgo, recent), false, StaleOff},
		"on via rules that all serve on": {flagWith(EnvConfig{Enabled: true, RolloutPercentage: 100, Rules: []eval.Rule{onRule}}, longAgo, recent), false, StaleOn},
		"0% with only off rules":         {flagWith(EnvConfig{Enabled: true, RolloutPercentage: 0, Rules: []eval.Rule{offRule}}, longAgo, recent), false, StaleOff},
		"a rule that differs":            {flagWith(EnvConfig{Enabled: true, RolloutPercentage: 100, Rules: []eval.Rule{offRule}}, longAgo, recent), false, ""},
		"partial rollout":                {flagWith(partial, longAgo, recent), false, ""},
		"recently changed somewhere":     {devOnly, false, ""},
		"prod decides when protected":    {prodOnDevOff, false, StaleOn},
		"protected prod off decides":     {mixed, false, StaleOff}, // dev is on
		"unused":                         {flagWith(partial, recent, longAgo), false, StaleUnused},
		"used in one environment":        {usedInProd, false, StaleOn},
		"pending request":                {flagWith(on, longAgo, recent), true, ""},
		"permanent":                      {permanent, false, ""},
		"archived":                       {archived, false, ""},
	} {
		got := Assess(c.f, envs, now, after, c.pending)
		if got.Reason != c.want {
			t.Errorf("%s: got %q, want %q", name, got.Reason, c.want)
		}
		if got.Stale() && got.Suggestion == "" {
			t.Errorf("%s: no suggestion", name)
		}
	}
}

func TestAssessMixedWhenNothingDecides(t *testing.T) {
	noProtected := []Environment{{Key: "dev"}, {Key: "staging"}}
	f := flagWith(off, longAgo, recent)
	f.Environments["dev"] = on
	if got := Assess(f, noProtected, now, after, false); got.Reason != StaleMixed {
		t.Errorf("got %q, want settled_mixed", got.Reason)
	}
}

func TestAssessSince(t *testing.T) {
	changed := now.Add(-31 * 24 * time.Hour)
	got := Assess(flagWith(on, changed, recent), envs, now, after, false)
	if want := changed.Add(after); !got.Since.Equal(want) {
		t.Errorf("settled since %v, want %v", got.Since, want)
	}
	evaluated := now.Add(-35 * 24 * time.Hour)
	got = Assess(flagWith(partial, longAgo, evaluated), envs, now, after, false)
	if want := evaluated.Add(after); got.Reason != StaleUnused || !got.Since.Equal(want) {
		t.Errorf("unused since %v (%q), want %v", got.Since, got.Reason, want)
	}
	// Exactly at the threshold counts as stale.
	if got := Assess(flagWith(on, now.Add(-after), recent), envs, now, after, false); got.Reason != StaleOn {
		t.Errorf("at the threshold: %q", got.Reason)
	}
}

func TestValidatePermanentReason(t *testing.T) {
	if r, err := ValidatePermanentReason("  ops kill switch  "); err != nil || r != "ops kill switch" {
		t.Errorf("got %q, %v", r, err)
	}
	long := make([]rune, 501)
	for i := range long {
		long[i] = 'é'
	}
	if _, err := ValidatePermanentReason(string(long)); err == nil {
		t.Error("501 characters accepted")
	}
	if _, err := ValidatePermanentReason(string(long[:500])); err != nil {
		t.Errorf("500 characters: %v", err)
	}
}
