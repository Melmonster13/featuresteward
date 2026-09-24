package main

import (
	"errors"
	"fmt"
	"slices"
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
	if err := e.parse(e.flags("whoami"), args); err != nil {
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
	fmt.Fprintf(e.stdout, "%s (%s) at %s\n", u.Handle, u.Role, cfg.URL)
	return nil
}

func cmdList(e *env, args []string) error {
	fs := e.flags("list")
	envName := fs.String("env", "", "show only this environment")
	steward := fs.String("steward", "", "only flags with this steward, or none for unassigned")
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
			return fmt.Errorf("unknown environment %q (have: %s)", *envName, strings.Join(keys, ", "))
		}
		keys = []string{*envName}
	}
	flags, err := c.ListFlags(e.ctx, *steward)
	if err != nil {
		return err
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
