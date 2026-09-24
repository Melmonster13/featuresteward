// Package eval decides whether a flag is on for a given user.
package eval

import (
	"crypto/sha256"
	"encoding/binary"
	"slices"
)

type Reason string

const (
	ReasonDisabled          Reason = "disabled"
	ReasonRuleMatch         Reason = "rule_match"
	ReasonEnabled           Reason = "enabled"
	ReasonPercentageRollout Reason = "percentage_rollout"
	ReasonMissingUserID     Reason = "missing_user_id"
)

const (
	AttributeUserID = "user_id"
	AttributeGroup  = "group"
)

// Rule serves Serve to any user whose Attribute matches one of Values.
type Rule struct {
	Attribute string   `json:"attribute"`
	Values    []string `json:"values"`
	Serve     bool     `json:"serve"`
}

// Flag is a flag's configuration in one environment.
type Flag struct {
	Key               string
	Enabled           bool
	RolloutPercentage int
	Rules             []Rule
}

type Context struct {
	UserID string
	Groups []string
}

type Result struct {
	Enabled bool
	Reason  Reason
}

// Evaluate checks, in order: the environment toggle, targeting rules
// (first match wins), then the percentage rollout.
func Evaluate(f Flag, c Context) Result {
	if !f.Enabled {
		return Result{false, ReasonDisabled}
	}
	for _, r := range f.Rules {
		if r.matches(c) {
			return Result{r.Serve, ReasonRuleMatch}
		}
	}
	if f.RolloutPercentage >= 100 {
		return Result{true, ReasonEnabled}
	}
	if c.UserID == "" {
		return Result{false, ReasonMissingUserID}
	}
	return Result{Bucket(f.Key, c.UserID) < f.RolloutPercentage, ReasonPercentageRollout}
}

func (r Rule) matches(c Context) bool {
	switch r.Attribute {
	case AttributeUserID:
		return c.UserID != "" && slices.Contains(r.Values, c.UserID)
	case AttributeGroup:
		return slices.ContainsFunc(c.Groups, func(g string) bool { return slices.Contains(r.Values, g) })
	}
	return false
}

// Bucket maps a user to a stable value in [0, 100) for a flag.
// Changing this reshuffles every rollout, so it is pinned by tests.
func Bucket(flagKey, userID string) int {
	sum := sha256.Sum256([]byte(flagKey + userID))
	return int(binary.BigEndian.Uint32(sum[:4]) % 100)
}
