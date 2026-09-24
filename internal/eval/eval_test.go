package eval

import (
	"fmt"
	"testing"
)

// Computed outside Go (shasum) so a change to Bucket can't silently
// reshuffle existing rollouts.
func TestBucketGolden(t *testing.T) {
	tests := []struct {
		flag, user string
		want       int
	}{
		{"new-checkout", "user-42", 14},
		{"new-checkout", "user-1", 66},
		{"dark-mode", "user-42", 18},
	}
	for _, tt := range tests {
		if got := Bucket(tt.flag, tt.user); got != tt.want {
			t.Errorf("Bucket(%q, %q) = %d, want %d", tt.flag, tt.user, got, tt.want)
		}
	}
}

func TestEvaluate(t *testing.T) {
	// user-42 is in bucket 14 for new-checkout.
	tests := []struct {
		name string
		flag Flag
		ctx  Context
		want Result
	}{
		{
			name: "disabled beats everything",
			flag: Flag{Key: "new-checkout", Enabled: false, RolloutPercentage: 100,
				Rules: []Rule{{Attribute: AttributeUserID, Values: []string{"user-42"}, Serve: true}}},
			ctx:  Context{UserID: "user-42"},
			want: Result{false, ReasonDisabled},
		},
		{
			name: "fully enabled",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 100},
			ctx:  Context{UserID: "user-42"},
			want: Result{true, ReasonEnabled},
		},
		{
			name: "fully enabled without user id",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 100},
			want: Result{true, ReasonEnabled},
		},
		{
			name: "user in rollout",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 15},
			ctx:  Context{UserID: "user-42"},
			want: Result{true, ReasonPercentageRollout},
		},
		{
			name: "user just outside rollout",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 14},
			ctx:  Context{UserID: "user-42"},
			want: Result{false, ReasonPercentageRollout},
		},
		{
			name: "zero percent",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 0},
			ctx:  Context{UserID: "user-42"},
			want: Result{false, ReasonPercentageRollout},
		},
		{
			name: "partial rollout needs a user id",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 50},
			want: Result{false, ReasonMissingUserID},
		},
		{
			name: "user id rule overrides rollout",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 0,
				Rules: []Rule{{Attribute: AttributeUserID, Values: []string{"user-42"}, Serve: true}}},
			ctx:  Context{UserID: "user-42"},
			want: Result{true, ReasonRuleMatch},
		},
		{
			name: "rule can exclude",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 100,
				Rules: []Rule{{Attribute: AttributeUserID, Values: []string{"user-42"}, Serve: false}}},
			ctx:  Context{UserID: "user-42"},
			want: Result{false, ReasonRuleMatch},
		},
		{
			name: "group rule",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 0,
				Rules: []Rule{{Attribute: AttributeGroup, Values: []string{"beta", "staff"}, Serve: true}}},
			ctx:  Context{UserID: "user-42", Groups: []string{"free", "staff"}},
			want: Result{true, ReasonRuleMatch},
		},
		{
			name: "first matching rule wins",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 0,
				Rules: []Rule{
					{Attribute: AttributeUserID, Values: []string{"user-42"}, Serve: false},
					{Attribute: AttributeGroup, Values: []string{"staff"}, Serve: true},
				}},
			ctx:  Context{UserID: "user-42", Groups: []string{"staff"}},
			want: Result{false, ReasonRuleMatch},
		},
		{
			name: "non-matching rules fall through",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 15,
				Rules: []Rule{
					{Attribute: AttributeGroup, Values: []string{"beta"}, Serve: false},
					{Attribute: "country", Values: []string{"NZ"}, Serve: false},
				}},
			ctx:  Context{UserID: "user-42", Groups: []string{"staff"}},
			want: Result{true, ReasonPercentageRollout},
		},
		{
			name: "empty user id never matches user rule",
			flag: Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: 100,
				Rules: []Rule{{Attribute: AttributeUserID, Values: []string{""}, Serve: false}}},
			want: Result{true, ReasonEnabled},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Evaluate(tt.flag, tt.ctx); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Raising the percentage must only add users, never remove them.
func TestRolloutIsMonotonic(t *testing.T) {
	for i := range 1000 {
		user := fmt.Sprintf("user-%d", i)
		wasOn := false
		for pct := 0; pct <= 100; pct++ {
			on := Evaluate(Flag{Key: "new-checkout", Enabled: true, RolloutPercentage: pct}, Context{UserID: user}).Enabled
			if wasOn && !on {
				t.Fatalf("%s lost the flag going to %d%%", user, pct)
			}
			wasOn = on
		}
	}
}

func TestRolloutDistribution(t *testing.T) {
	const n = 20000
	on := 0
	for i := range n {
		if Bucket("new-checkout", fmt.Sprintf("user-%d", i)) < 25 {
			on++
		}
	}
	if pct := float64(on) / n * 100; pct < 24 || pct > 26 {
		t.Errorf("25%% rollout enabled %.2f%% of users", pct)
	}
}
