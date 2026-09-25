// Package digest tells stewards about their stale flags once a day, by
// posting a Slack-compatible message to a webhook. Each flag is repeated
// at most once a week.
package digest

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Melmonster13/featuresteward/internal/flag"
)

// JobName is the digest's row in the jobs table.
const JobName = "stale_digest"

// Remind is how often a flag that stays stale is mentioned again.
const Remind = 7 * 24 * time.Hour

// Store is the part of flag.Store the digest uses.
type Store interface {
	ListFlags(ctx context.Context) ([]flag.Flag, error)
	ListEnvironments(ctx context.Context) ([]flag.Environment, error)
	ListChangeRequests(ctx context.Context, filter flag.RequestFilter) ([]flag.ChangeRequest, error)
	MarkStaleNotified(ctx context.Context, keys []string, at time.Time) error
}

// Claimer lets one server claim a job's run per period.
type Claimer interface {
	ClaimJob(ctx context.Context, name string, every time.Duration) (bool, error)
	// ReleaseJob gives up a claim, so the job can run again right away.
	ReleaseJob(ctx context.Context, name string) error
}

type Digest struct {
	Flags  Store
	Claim  Claimer
	Send   func(ctx context.Context, text string) error
	After  time.Duration // the stale threshold
	Every  time.Duration // how often the digest goes out
	Public string        // dashboard URL for links; "" for none
	Log    *slog.Logger
	now    func() time.Time
}

// Run sends the digest if it's due and this server claims it.
func (d *Digest) Run(ctx context.Context) error {
	claimed, err := d.Claim.ClaimJob(ctx, JobName, d.Every)
	if err != nil || !claimed {
		return err
	}
	now := d.clock()
	flags, err := d.Flags.ListFlags(ctx)
	if err != nil {
		return err
	}
	envs, err := d.Flags.ListEnvironments(ctx)
	if err != nil {
		return err
	}
	rs, err := d.Flags.ListChangeRequests(ctx, flag.RequestFilter{Status: flag.RequestPending})
	if err != nil {
		return err
	}
	pending := map[string]bool{}
	for _, r := range rs {
		pending[r.FlagKey] = true
	}
	text, keys := d.Build(flags, envs, pending, now)
	if len(keys) == 0 {
		return nil
	}
	// If sending fails, give the claim back so the next hourly check
	// retries; the flags aren't marked, so they'll be included.
	if err := d.Send(ctx, text); err != nil {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rerr := d.Claim.ReleaseJob(release, JobName); rerr != nil {
			d.Log.Error("release the stale digest job", "err", rerr)
		}
		return err
	}
	if err := d.Flags.MarkStaleNotified(ctx, keys, now); err != nil {
		return err
	}
	d.Log.Info("sent the stale flag digest", "flags", len(keys))
	return nil
}

// Build writes the message for flags that are stale and haven't been
// mentioned within Remind, grouped by steward. It uses flag keys and
// handles only: names are free text and could carry Slack mentions.
func (d *Digest) Build(flags []flag.Flag, envs []flag.Environment, pending map[string]bool, now time.Time) (string, []string) {
	type item struct {
		key string
		st  flag.Staleness
	}
	byOwner := map[string][]item{}
	var keys []string
	for _, f := range flags {
		st := flag.Assess(f, envs, now, d.After, pending[f.Key])
		if !st.Stale() || (f.StaleNotifiedAt != nil && now.Sub(*f.StaleNotifiedAt) < Remind) {
			continue
		}
		byOwner[f.Steward] = append(byOwner[f.Steward], item{f.Key, st})
		keys = append(keys, f.Key)
	}
	if len(keys) == 0 {
		return "", nil
	}
	owners := make([]string, 0, len(byOwner))
	for o := range byOwner {
		owners = append(owners, o)
	}
	sort.Strings(owners) // "" (no steward) sorts first

	var b strings.Builder
	fmt.Fprintf(&b, "*FeatureSteward: %d stale flag%s*\n", len(keys), plural(len(keys)))
	for _, o := range owners {
		if o == "" {
			b.WriteString("\n*No steward*\n")
		} else {
			fmt.Fprintf(&b, "\n*@%s*\n", o)
		}
		items := byOwner[o]
		sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })
		for _, it := range items {
			fmt.Fprintf(&b, "• %s — %s since %s. %s\n", d.link(it.key), label(it.st.Reason), it.st.Since.UTC().Format("Jan 2"), it.st.Suggestion)
		}
	}
	return b.String(), keys
}

func (d *Digest) link(key string) string {
	if d.Public == "" {
		return "`" + key + "`"
	}
	return "<" + strings.TrimRight(d.Public, "/") + "/#/flags/" + key + "|" + key + ">"
}

func label(r flag.StaleReason) string {
	switch r {
	case flag.StaleUnused:
		return "unused"
	case flag.StaleOn:
		return "on for everyone"
	case flag.StaleOff:
		return "off for everyone"
	default:
		return "settled"
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (d *Digest) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}
