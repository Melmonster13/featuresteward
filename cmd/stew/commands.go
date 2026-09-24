package main

import (
	"errors"
	"fmt"
	"net/http"
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
	flags, err := c.ListFlags(e.ctx, *steward)
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

func cmdToggle(e *env, args []string) error {
	fs := e.flags("toggle")
	asJSON := fs.Bool("json", false, "print the updated flag as JSON")
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
	return updateEnv(e, *asJSON, pos[0], pos[1], func(cfg *client.EnvConfig) { cfg.Enabled = on })
}

func cmdRollout(e *env, args []string) error {
	fs := e.flags("rollout")
	asJSON := fs.Bool("json", false, "print the updated flag as JSON")
	pos, err := e.parseArgs(fs, args, "flag", "env", "percent")
	if err != nil {
		return err
	}
	pct, err := strconv.Atoi(strings.TrimSuffix(pos[2], "%"))
	if err != nil || pct < 0 || pct > 100 {
		fmt.Fprintf(e.stderr, "stew rollout: percent must be a whole number 0-100, got %q\n", pos[2])
		return errUsage
	}
	return updateEnv(e, *asJSON, pos[0], pos[1], func(cfg *client.EnvConfig) { cfg.RolloutPercentage = pct })
}

// updateEnv changes one field of a flag's environment config and keeps
// the rest, since the API replaces the whole config.
func updateEnv(e *env, asJSON bool, key, envKey string, change func(*client.EnvConfig)) error {
	c, _, err := e.client()
	if err != nil {
		return err
	}
	f, err := c.Flag(e.ctx, key)
	if err != nil {
		return flagErr(err, key)
	}
	cfg, ok := f.Environments[envKey]
	if !ok {
		return notFound("unknown environment %q", envKey)
	}
	change(&cfg)
	f, err = c.SetEnvironment(e.ctx, key, envKey, cfg)
	if err != nil {
		return err
	}
	cfg = f.Environments[envKey]
	if asJSON {
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
