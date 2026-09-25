package flag

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Melmonster13/featuresteward/internal/errs"
)

// StaleReason says why a flag is stale; "" means it isn't.
type StaleReason string

const (
	// StaleUnused: nothing has evaluated the flag anywhere for a while.
	StaleUnused StaleReason = "unused"
	// StaleOn and StaleOff: every environment has served the same value
	// to everyone, unchanged, for a while.
	StaleOn  StaleReason = "always_on"
	StaleOff StaleReason = "always_off"
	// StaleMixed: settled, but on in some environments and off in others.
	StaleMixed StaleReason = "settled_mixed"
)

// Staleness is a flag's stale assessment.
type Staleness struct {
	Reason StaleReason
	// Since is when the flag became stale.
	Since      time.Time
	Suggestion string
}

func (s Staleness) Stale() bool { return s.Reason != "" }

var suggestions = map[StaleReason]string{
	StaleUnused: "Nothing has evaluated it recently. Check that the code no longer uses it, then archive it.",
	StaleOn:     "It has been on for everyone, unchanged, for a while. Remove the flag and keep the new code.",
	StaleOff:    "It has been off for everyone, unchanged, for a while. Remove the flag and the code behind it.",
	StaleMixed:  "It's settled, but on in some environments and off in others. Decide on one, then remove the flag.",
}

// Assess reports whether a flag is stale: not permanent or archived, no
// change request pending, and either unused or settled for at least after.
func Assess(f Flag, envs []Environment, now time.Time, after time.Duration, pending bool) Staleness {
	if f.ArchivedAt != nil || f.PermanentReason != "" || pending || len(f.Activity) == 0 {
		return Staleness{}
	}
	var lastEval, lastChange time.Time
	for _, a := range f.Activity {
		lastEval = latest(lastEval, a.EvaluatedAt)
		lastChange = latest(lastChange, a.ChangedAt)
	}
	if since := lastEval.Add(after); !now.Before(since) {
		return Staleness{Reason: StaleUnused, Since: since, Suggestion: suggestions[StaleUnused]}
	}
	since := lastChange.Add(after)
	if now.Before(since) {
		return Staleness{}
	}
	value, ok := settledValue(f, envs)
	if !ok {
		return Staleness{}
	}
	return Staleness{Reason: value, Since: since, Suggestion: suggestions[value]}
}

// settledValue says what the flag serves if every environment serves one
// value to everyone. Protected environments decide on and off when they
// agree; otherwise all environments must.
func settledValue(f Flag, envs []Environment) (StaleReason, bool) {
	var all, protected []bool
	for _, e := range envs {
		cfg, ok := f.Environments[e.Key]
		if !ok {
			continue
		}
		v, ok := uniform(cfg)
		if !ok {
			return "", false
		}
		all = append(all, v)
		if e.Protected {
			protected = append(protected, v)
		}
	}
	for _, vs := range [][]bool{protected, all} {
		if len(vs) > 0 && same(vs) {
			if vs[0] {
				return StaleOn, true
			}
			return StaleOff, true
		}
	}
	return StaleMixed, len(all) > 0
}

// uniform says what cfg serves if it serves the same value to everyone.
func uniform(cfg EnvConfig) (bool, bool) {
	if !cfg.Enabled {
		return false, true
	}
	var v bool
	switch cfg.RolloutPercentage {
	case 100:
		v = true
	case 0:
	default:
		return false, false
	}
	for _, r := range cfg.Rules {
		if r.Serve != v {
			return false, false
		}
	}
	return v, true
}

func same(vs []bool) bool {
	for _, v := range vs {
		if v != vs[0] {
			return false
		}
	}
	return true
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// PermanentSnapshot is the audit record of a permanent mark.
type PermanentSnapshot struct {
	Reason *string `json:"permanent_reason"` // null when not permanent
}

func NewPermanentSnapshot(reason string) PermanentSnapshot {
	if reason == "" {
		return PermanentSnapshot{}
	}
	return PermanentSnapshot{Reason: &reason}
}

// ValidatePermanentReason trims a reason and checks its length.
func ValidatePermanentReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 500 {
		return "", errs.Invalid("the permanent reason must be 500 characters or fewer")
	}
	return reason, nil
}
