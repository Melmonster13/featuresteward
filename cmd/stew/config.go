package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
)

// config is what `stew login` saves. STEW_URL and STEW_TOKEN override it.
type config struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	// Insecure allows a plain-http URL to a non-local host (login --insecure).
	Insecure bool `json:"insecure,omitempty"`
}

// configPath is $XDG_CONFIG_HOME/stew/config.json, else ~/.config/stew/config.json.
func configPath(getenv func(string) string) (string, error) {
	dir := getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("can't find a config directory: set HOME or XDG_CONFIG_HOME")
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "stew", "config.json"), nil
}

// loadConfig reads the saved config and applies environment overrides.
// warn receives a message if the file is readable by other users.
func loadConfig(getenv func(string) string, warn func(string)) (config, error) {
	var c config
	path, err := configPath(getenv)
	if err != nil {
		return c, err
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return c, err
	default:
		if err := json.Unmarshal(data, &c); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
		if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
			warn(fmt.Sprintf("warning: %s is readable by other users; run: chmod 600 %s", path, path))
		}
	}
	if v := getenv("STEW_URL"); v != "" {
		c.URL = v
	}
	if v := getenv("STEW_TOKEN"); v != "" {
		c.Token = v
	}
	if getenv("STEW_INSECURE") == "1" {
		c.Insecure = true
	}
	if c.URL == "" || c.Token == "" {
		return c, &codedError{exitAuth, "not logged in: run `stew login --url <api-url>`, or set STEW_URL and STEW_TOKEN"}
	}
	return c, checkURL(c.URL, c.Insecure)
}

// saveConfig writes the config readable only by the current user.
func saveConfig(getenv func(string) string, c config) (string, error) {
	path, err := configPath(getenv)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	// Write then rename so a crash can't leave a half-written file.
	// CreateTemp already uses 0600; the Chmod keeps that explicit.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}

func removeConfig(getenv func(string) string) (string, error) {
	path, err := configPath(getenv)
	if err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return path, nil
}

// checkURL requires an absolute http(s) URL, and https unless the host
// is local, since the token is sent with every request.
func checkURL(raw string, insecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q isn't a valid URL; expected something like https://flags.example.com", raw)
	}
	if u.Scheme == "http" && !insecure {
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
		default:
			return fmt.Errorf("refusing to send a token over plain http to %s; use https, or pass --insecure (STEW_INSECURE=1)", u.Host)
		}
	}
	return nil
}
