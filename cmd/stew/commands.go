package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"reflect"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Melmonster13/featuresteward/internal/client"
)

func cmdLogin(e *env, args []string) error {
	fs := e.flags("login")
	url := fs.String("url", "", "FeatureSteward API URL, e.g. https://flags.example.com (required)")
	insecure := fs.Bool("insecure", false, "allow plain http to a non-local host")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	if *url == "" {
		fmt.Fprintln(e.stderr, "stew login: --url is required")
		fs.Usage()
		return errUsage
	}
	if err := checkURL(*url, *insecure); err != nil {
		return err
	}
	// The token comes from stdin, never a flag, so it stays out of shell history.
	token, err := e.readSecret("API token: ")
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("no token given; paste it at the prompt or pipe it to stdin")
	}
	u, err := client.New(*url, token).Me(e.ctx)
	if err != nil {
		return fmt.Errorf("checking token: %w", err)
	}
	path, err := saveConfig(e.getenv, config{URL: *url, Token: token, Insecure: *insecure})
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Logged in to %s as %s (%s). Saved to %s\n", *url, u.Handle, u.Role, path)
	return nil
}

// cmdLogout revokes the saved token on the server, then deletes it locally.
func cmdLogout(e *env, args []string) error {
	fs := e.flags("logout")
	keep := fs.Bool("keep-token", false, "delete the saved token locally without revoking it")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	saved, err := loadConfig(func(k string) string {
		if strings.HasPrefix(k, "STEW_") {
			return "" // only the saved login, not environment overrides
		}
		return e.getenv(k)
	}, func(string) {})
	if err == nil && !*keep {
		if err := revokeOwnToken(e, saved); err != nil {
			fmt.Fprintf(e.stderr, "warning: couldn't revoke the token (%v); revoke it with the API or ask an admin\n", err)
		}
	}
	path, err := removeConfig(e.getenv)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Logged out. Removed %s\n", path)
	return nil
}

// revokeOwnToken finds the token by its display prefix and revokes it.
func revokeOwnToken(e *env, cfg config) error {
	c := client.New(cfg.URL, cfg.Token)
	toks, err := c.ListMyTokens(e.ctx)
	if err != nil {
		return err
	}
	var match []client.Token
	for _, t := range toks {
		if t.RevokedAt == nil && t.Prefix != "" && strings.HasPrefix(cfg.Token, t.Prefix) {
			match = append(match, t)
		}
	}
	if len(match) != 1 {
		return fmt.Errorf("found %d tokens matching %s…", len(match), cfg.Token[:min(len(cfg.Token), 11)])
	}
	return c.RevokeMyToken(e.ctx, match[0].ID)
}

func cmdWhoami(e *env, args []string) error {
	fs := e.flags("whoami")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	c, cfg, err := e.client()
	if err != nil {
		return err
	}
	u, err := c.Me(e.ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return e.writeJSON(map[string]string{"handle": u.Handle, "name": u.Name, "role": u.Role, "url": cfg.URL})
	}
	fmt.Fprintf(e.stdout, "%s (%s) at %s\n", u.Handle, u.Role, cfg.URL)
	return nil
}

func cmdList(e *env, args []string) error {
	fs := e.flags("list")
	envName := fs.String("env", "", "show only this environment")
	steward := fs.String("steward", "", "only flags with this steward, or none for unassigned")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	c, _, err := e.client()
	if err != nil {
		return err
	}
	envs, err := c.ListEnvironments(e.ctx)
	if err != nil {
		return err
	}
	var keys []string
	for _, en := range envs {
		keys = append(keys, en.Key)
	}
	if *envName != "" {
		if !slices.Contains(keys, *envName) {
			return notFound("unknown environment %q (have: %s)", *envName, strings.Join(keys, ", "))
		}
		keys = []string{*envName}
	}
	flags, err := c.ListFlags(e.ctx, *steward, false)
	if err != nil {
		return err
	}
	if *asJSON {
		if *envName != "" {
			for i := range flags {
				flags[i].Environments = map[string]client.EnvConfig{*envName: flags[i].Environments[*envName]}
			}
		}
		if flags == nil {
			flags = []client.Flag{}
		}
		return e.writeJSON(map[string]any{"flags": flags})
	}
	if len(flags) == 0 {
		fmt.Fprintln(e.stderr, "No flags.")
		return nil
	}

	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "KEY\t%s\tSTEWARD\n", strings.ToUpper(strings.Join(keys, "\t")))
	for _, f := range flags {
		cols := []string{f.Key}
		for _, k := range keys {
			cols = append(cols, state(f.Environments[k]))
		}
		cols = append(cols, stewardName(f.Steward))
		fmt.Fprintln(tw, strings.Join(cols, "\t"))
	}
	return tw.Flush()
}

// state summarizes a flag in one environment: off, on, 25%, or with
// "+N rules" when targeting rules apply.
func state(c client.EnvConfig) string {
	s := "off"
	if c.Enabled {
		s = "on"
		if c.RolloutPercentage < 100 {
			s = fmt.Sprintf("%d%%", c.RolloutPercentage)
		}
		if n := len(c.Rules); n > 0 {
			s += fmt.Sprintf(" +%d rule", n)
			if n > 1 {
				s += "s"
			}
		}
	}
	return s
}

// detail is like state, but also says what an off flag would serve once
// turned on, so a change to an off flag is visible: "off (30% when on)".
func detail(c client.EnvConfig) string {
	if c.Enabled {
		return state(c)
	}
	c.Enabled = true
	if on := state(c); on != "on" {
		return "off (" + on + " when on)"
	}
	return "off"
}

func stewardName(s *string) string {
	if s == nil {
		return "(none)"
	}
	return "@" + *s
}

func cmdStatus(e *env, args []string) error {
	fs := e.flags("status")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := e.parseArgs(fs, args, "flag")
	if err != nil {
		return err
	}
	c, _, err := e.client()
	if err != nil {
		return err
	}
	f, err := c.Flag(e.ctx, pos[0])
	if err != nil {
		return flagErr(err, pos[0])
	}
	if *asJSON {
		return e.writeJSON(f)
	}
	envs, err := c.ListEnvironments(e.ctx)
	if err != nil {
		return err
	}
	printFlag(e, f, envs)
	return nil
}

func printFlag(e *env, f client.Flag, envs []client.Environment) {
	fmt.Fprintf(e.stdout, "%s  %s\n", f.Key, f.Name)
	if f.Description != "" {
		fmt.Fprintf(e.stdout, "  %s\n", f.Description)
	}
	fmt.Fprintf(e.stdout, "Steward: %s\n", stewardName(f.Steward))
	if f.ArchivedAt != nil {
		fmt.Fprintf(e.stdout, "Archived: %s\n", f.ArchivedAt.Format("2006-01-02 15:04 MST"))
	}
	if f.PermanentReason != nil {
		fmt.Fprintf(e.stdout, "Permanent: %s\n", *f.PermanentReason)
	}
	if f.Stale != nil {
		fmt.Fprintf(e.stdout, "Stale: %s since %s. %s\n", staleLabel(f.Stale.Reason), f.Stale.Since.Format("2006-01-02"), f.Stale.Suggestion)
	}
	fmt.Fprintln(e.stdout)
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ENV\tSTATE\tRULES")
	for _, en := range envs {
		cfg := f.Environments[en.Key]
		st := "off"
		if cfg.Enabled {
			st = fmt.Sprintf("on %d%%", cfg.RolloutPercentage)
		}
		name := en.Key
		if en.Protected {
			name += " (protected)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", name, st, rules(cfg.Rules))
	}
	tw.Flush()
}

// rules renders targeting rules, e.g. "group in [staff] → on".
func rules(rs []client.Rule) string {
	if len(rs) == 0 {
		return "-"
	}
	var out []string
	for _, r := range rs {
		serve := "off"
		if r.Serve {
			serve = "on"
		}
		out = append(out, fmt.Sprintf("%s in [%s] → %s", r.Attribute, strings.Join(r.Values, ", "), serve))
	}
	return strings.Join(out, "; ")
}

func cmdCreate(e *env, args []string) error {
	fs := e.flags("create")
	name := fs.String("name", "", "human-readable name (required)")
	desc := fs.String("description", "", "what the flag is for")
	steward := fs.String("steward", "", "steward handle (default: you)")
	asJSON := fs.Bool("json", false, "print the new flag as JSON")
	pos, err := e.parseArgs(fs, args, "flag")
	if err != nil {
		return err
	}
	if *name == "" {
		fmt.Fprintln(e.stderr, "stew create: --name is required")
		fs.Usage()
		return errUsage
	}
	c, _, err := e.client()
	if err != nil {
		return err
	}
	f, err := c.CreateFlag(e.ctx, pos[0], *name, *desc, strings.TrimPrefix(*steward, "@"))
	if err != nil {
		return err
	}
	if *asJSON {
		return e.writeJSON(f)
	}
	fmt.Fprintf(e.stdout, "Created %s (steward %s). It's off in every environment.\n", f.Key, stewardName(f.Steward))
	return nil
}

// changeOpts are the flags shared by toggle and rollout.
type changeOpts struct {
	asJSON            *bool
	reason, emergency *string
}

func changeFlags(fs *flag.FlagSet) changeOpts {
	return changeOpts{
		asJSON:    fs.Bool("json", false, "print the updated flag, or the new change request, as JSON"),
		reason:    fs.String("reason", "", "why, for the reviewer of a change request"),
		emergency: fs.String("emergency", "", "admins: apply a protected-environment change now, for this reason"),
	}
}

func cmdToggle(e *env, args []string) error {
	fs := e.flags("toggle")
	opts := changeFlags(fs)
	pos, err := e.parseArgs(fs, args, "flag", "env", "on|off")
	if err != nil {
		return err
	}
	var on bool
	switch pos[2] {
	case "on":
		on = true
	case "off":
	default:
		fmt.Fprintf(e.stderr, "stew toggle: expected on or off, got %q\n", pos[2])
		return errUsage
	}
	return updateEnv(e, opts, pos[0], pos[1], func(cfg *client.EnvConfig) { cfg.Enabled = on })
}

func cmdRollout(e *env, args []string) error {
	fs := e.flags("rollout")
	opts := changeFlags(fs)
	pos, err := e.parseArgs(fs, args, "flag", "env", "percent")
	if err != nil {
		return err
	}
	pct, err := strconv.Atoi(strings.TrimSuffix(pos[2], "%"))
	if err != nil || pct < 0 || pct > 100 {
		fmt.Fprintf(e.stderr, "stew rollout: percent must be a whole number 0-100, got %q\n", pos[2])
		return errUsage
	}
	return updateEnv(e, opts, pos[0], pos[1], func(cfg *client.EnvConfig) { cfg.RolloutPercentage = pct })
}

// updateEnv changes one field of a flag's environment config and keeps
// the rest, since the API replaces the whole config. In a protected
// environment it files a change request instead, unless the change only
// turns the flag off or is an admin's emergency.
func updateEnv(e *env, o changeOpts, key, envKey string, change func(*client.EnvConfig)) error {
	c, _, err := e.client()
	if err != nil {
		return err
	}
	f, err := c.Flag(e.ctx, key)
	if err != nil {
		return flagErr(err, key)
	}
	cur, ok := f.Environments[envKey]
	if !ok {
		return notFound("unknown environment %q", envKey)
	}
	envs, err := c.ListEnvironments(e.ctx)
	if err != nil {
		return err
	}
	protected := false
	for _, en := range envs {
		protected = protected || (en.Key == envKey && en.Protected)
	}
	cfg := cur
	change(&cfg)
	emergency := strings.TrimSpace(*o.emergency)
	if protected && emergency == "" && !killSwitch(cur, cfg) {
		r, err := c.RequestChange(e.ctx, key, envKey, cfg, strings.TrimSpace(*o.reason))
		if err != nil {
			return flagErr(err, key)
		}
		if *o.asJSON {
			return e.writeJSON(r)
		}
		fmt.Fprintf(e.stdout, "Requested #%d: %s in %s → %s.\n", r.ID, key, envKey, detail(r.Proposed))
		fmt.Fprintf(e.stdout, "It changes once the steward or an approver approves it. See: stew requests\n")
		return nil
	}
	if !protected {
		emergency = "" // only protected environments take a reason
	}
	f, err = c.SetEnvironment(e.ctx, key, envKey, cfg, emergency)
	if err != nil {
		return err
	}
	cfg = f.Environments[envKey]
	if *o.asJSON {
		if err := e.writeJSON(f); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(e.stdout, "%s in %s: %s\n", key, envKey, state(cfg))
	}
	if cfg.Enabled && cfg.RolloutPercentage == 0 && len(cfg.Rules) == 0 {
		fmt.Fprintf(e.stderr, "note: the rollout is 0%%, so nobody gets it yet; run: stew rollout %s %s <percent>\n", key, envKey)
	}
	return nil
}

func cmdSteward(e *env, args []string) error {
	fs := e.flags("steward")
	asJSON := fs.Bool("json", false, "print the updated flag as JSON")
	pos, err := e.parseArgs(fs, args, "flag", "handle")
	if err != nil {
		return err
	}
	c, _, err := e.client()
	if err != nil {
		return err
	}
	f, err := c.SetSteward(e.ctx, pos[0], strings.TrimPrefix(pos[1], "@"))
	if err != nil {
		return flagErr(err, pos[0])
	}
	if *asJSON {
		return e.writeJSON(f)
	}
	fmt.Fprintf(e.stdout, "%s is now stewarded by %s\n", f.Key, stewardName(f.Steward))
	return nil
}

func cmdArchive(e *env, args []string) error {
	fs := e.flags("archive")
	yes := fs.Bool("yes", false, "confirm archiving; evaluating the flag then returns not found")
	pos, err := e.parseArgs(fs, args, "flag")
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Fprintf(e.stderr, "stew archive: after archiving, evaluating %s returns not found; rerun with --yes to confirm\n", pos[0])
		return errUsage
	}
	c, _, err := e.client()
	if err != nil {
		return err
	}
	if err := c.ArchiveFlag(e.ctx, pos[0]); err != nil {
		return flagErr(err, pos[0])
	}
	fmt.Fprintf(e.stdout, "Archived %s.\n", pos[0])
	return nil
}

// flagErr names the flag when the API says it doesn't exist.
func flagErr(err error, key string) error {
	var ae *client.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return notFound("flag %q not found", key)
	}
	return err
}

// cmdVersion prints the module version: a tag or a commit-based
// pseudo-version, plus "+dirty" for a build with uncommitted changes.
func cmdVersion(e *env, args []string) error {
	if err := e.parse(e.flags("version"), args); err != nil {
		return err
	}
	v := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		v = info.Main.Version
	}
	fmt.Fprintln(e.stdout, "stew", v)
	return nil
}

// killSwitch reports whether next only turns the flag off, which
// protected environments allow without approval.
func killSwitch(cur, next client.EnvConfig) bool {
	cur.Enabled = false
	return !next.Enabled && next.RolloutPercentage == cur.RolloutPercentage && reflect.DeepEqual(rulesOf(next), rulesOf(cur))
}

func rulesOf(c client.EnvConfig) []client.Rule {
	if len(c.Rules) == 0 {
		return nil
	}
	return c.Rules
}

func cmdRequests(e *env, args []string) error {
	fs := e.flags("requests")
	all := fs.Bool("all", false, "include approved, rejected, cancelled, and expired requests")
	flagKey := fs.String("flag", "", "only requests for this flag")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	c, _, err := e.client()
	if err != nil {
		return err
	}
	status := "pending"
	if *all {
		status = ""
	}
	rs, err := c.Requests(e.ctx, status, *flagKey)
	if err != nil {
		return err
	}
	if *asJSON {
		if rs == nil {
			rs = []client.ChangeRequest{}
		}
		return e.writeJSON(map[string]any{"requests": rs})
	}
	if len(rs) == 0 {
		if *all {
			fmt.Fprintln(e.stderr, "No change requests.")
		} else {
			fmt.Fprintln(e.stderr, "No pending change requests.")
		}
		return nil
	}
	me, err := c.Me(e.ctx)
	if err != nil {
		return err
	}
	flags, err := c.ListFlags(e.ctx, "", false)
	if err != nil {
		return err
	}
	stewards := map[string]string{}
	for _, f := range flags {
		if f.Steward != nil {
			stewards[f.Key] = *f.Steward
		}
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tFLAG\tENV\tBY\tCHANGE\tSTATUS\tYOU CAN")
	for _, r := range rs {
		you := ""
		switch {
		case r.Status != "pending":
		case r.RequestedBy == me.Handle:
			you = "cancel"
		case me.Role == "approver" || me.Role == "admin" || stewards[r.Flag] == me.Handle:
			you = "review"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t@%s\t%s → %s\t%s\t%s\n",
			r.ID, r.Flag, r.Environment, r.RequestedBy, detail(r.Base), detail(r.Proposed), r.Status, you)
	}
	return tw.Flush()
}

func cmdApprove(e *env, args []string) error {
	return review(e, "approve", args, func(c *client.Client, id int64, comment string) (client.ChangeRequest, error) {
		return c.Approve(e.ctx, id, comment)
	})
}

func cmdReject(e *env, args []string) error {
	return review(e, "reject", args, func(c *client.Client, id int64, comment string) (client.ChangeRequest, error) {
		return c.Reject(e.ctx, id, comment)
	})
}

func review(e *env, name string, args []string, act func(*client.Client, int64, string) (client.ChangeRequest, error)) error {
	fs := e.flags(name)
	comment := fs.String("comment", "", "a note for the requester")
	asJSON := fs.Bool("json", false, "print the request as JSON")
	pos, err := e.parseArgs(fs, args, "id")
	if err != nil {
		return err
	}
	id, err := requestID(e, name, pos[0])
	if err != nil {
		return err
	}
	c, _, err := e.client()
	if err != nil {
		return err
	}
	r, err := act(c, id, strings.TrimSpace(*comment))
	if err != nil {
		return requestErr(err, id)
	}
	if *asJSON {
		return e.writeJSON(r)
	}
	if r.Status == "approved" {
		fmt.Fprintf(e.stdout, "Approved #%d: %s in %s is now %s.\n", r.ID, r.Flag, r.Environment, detail(r.Proposed))
	} else {
		fmt.Fprintf(e.stdout, "Rejected #%d. %s in %s stays %s.\n", r.ID, r.Flag, r.Environment, detail(r.Base))
	}
	return nil
}

func cmdCancel(e *env, args []string) error {
	fs := e.flags("cancel")
	asJSON := fs.Bool("json", false, "print the request as JSON")
	pos, err := e.parseArgs(fs, args, "id")
	if err != nil {
		return err
	}
	id, err := requestID(e, "cancel", pos[0])
	if err != nil {
		return err
	}
	c, _, err := e.client()
	if err != nil {
		return err
	}
	r, err := c.Cancel(e.ctx, id)
	if err != nil {
		return requestErr(err, id)
	}
	if *asJSON {
		return e.writeJSON(r)
	}
	fmt.Fprintf(e.stdout, "Cancelled #%d.\n", r.ID)
	return nil
}

// requestID accepts 12 or #12.
func requestID(e *env, cmd, arg string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintf(e.stderr, "stew %s: expected a request ID like 12, got %q\n", cmd, arg)
		return 0, errUsage
	}
	return id, nil
}

// requestErr names the request when the API says it doesn't exist.
func requestErr(err error, id int64) error {
	var ae *client.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return notFound("request #%d not found", id)
	}
	return err
}

func cmdStale(e *env, args []string) error {
	fs := e.flags("stale")
	steward := fs.String("steward", "", "only flags with this steward: a handle, me, or none")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	c, _, err := e.client()
	if err != nil {
		return err
	}
	who := strings.TrimPrefix(*steward, "@")
	if who == "me" {
		me, err := c.Me(e.ctx)
		if err != nil {
			return err
		}
		who = me.Handle
	}
	flags, err := c.ListFlags(e.ctx, who, true)
	if err != nil {
		return err
	}
	if *asJSON {
		if flags == nil {
			flags = []client.Flag{}
		}
		return e.writeJSON(map[string]any{"flags": flags})
	}
	if len(flags) == 0 {
		fmt.Fprintln(e.stderr, "No stale flags.")
		return nil
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tSTEWARD\tSTALE\tSINCE")
	advice := map[string]string{}
	var reasons []string
	for _, f := range flags {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.Key, stewardName(f.Steward), staleLabel(f.Stale.Reason), f.Stale.Since.Format("2006-01-02"))
		if _, ok := advice[f.Stale.Reason]; !ok {
			reasons = append(reasons, f.Stale.Reason)
		}
		advice[f.Stale.Reason] = f.Stale.Suggestion
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(e.stdout)
	for _, r := range reasons {
		fmt.Fprintf(e.stdout, "%s: %s\n", staleLabel(r), advice[r])
	}
	return nil
}

func cmdPermanent(e *env, args []string) error {
	fs := e.flags("permanent")
	clear := fs.Bool("clear", false, "remove the permanent mark")
	asJSON := fs.Bool("json", false, "print the flag as JSON")
	var pos []string
	var err error
	if hasFlag(args, "clear") {
		pos, err = e.parseArgs(fs, args, "flag")
	} else {
		pos, err = e.parseArgs(fs, args, "flag", "reason")
	}
	if err != nil {
		return err
	}
	reason := ""
	if !*clear {
		reason = strings.TrimSpace(pos[1])
		if reason == "" {
			fmt.Fprintln(e.stderr, "stew permanent: give a reason, or use --clear")
			return errUsage
		}
	}
	c, _, err := e.client()
	if err != nil {
		return err
	}
	f, err := c.SetPermanent(e.ctx, pos[0], reason)
	if err != nil {
		return flagErr(err, pos[0])
	}
	if *asJSON {
		return e.writeJSON(f)
	}
	if reason == "" {
		fmt.Fprintf(e.stdout, "%s is no longer permanent; it can be reported stale again.\n", f.Key)
	} else {
		fmt.Fprintf(e.stdout, "%s is permanent: %s. It won't be reported stale.\n", f.Key, reason)
	}
	return nil
}

// hasFlag reports whether args include --name or -name.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name || a == "-"+name || strings.HasPrefix(a, "--"+name+"=") {
			return true
		}
	}
	return false
}

func staleLabel(reason string) string {
	switch reason {
	case "unused":
		return "unused"
	case "always_on":
		return "always on"
	case "always_off":
		return "always off"
	case "settled_mixed":
		return "settled"
	}
	return reason
}
