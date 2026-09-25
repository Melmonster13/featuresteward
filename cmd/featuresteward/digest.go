package main

import (
	"errors"
	"log/slog"
	"net/url"
	"os"
	"time"

	"github.com/Melmonster13/featuresteward/internal/digest"
	"github.com/Melmonster13/featuresteward/internal/store"
)

// newDigest returns the daily stale flag digest, or nil when
// STALE_WEBHOOK_URL isn't set.
func newDigest(db *store.Postgres, after time.Duration, log *slog.Logger) (*digest.Digest, error) {
	raw := os.Getenv("STALE_WEBHOOK_URL")
	if raw == "" {
		log.Info("STALE_WEBHOOK_URL is not set; stale flags are shown in the dashboard and stew only")
		return nil, nil
	}
	hook, err := digest.NewWebhook(raw)
	if err != nil {
		return nil, err
	}
	public := os.Getenv("PUBLIC_URL")
	if public != "" {
		if u, err := url.Parse(public); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, errors.New("PUBLIC_URL must be the dashboard's http(s) URL, like https://flags.example.com")
		}
	}
	return &digest.Digest{
		Flags: db, Claim: db, Send: hook.Send, After: after,
		// A little under a day, so an hourly check doesn't drift later.
		Every:  23 * time.Hour,
		Public: public, Log: log,
	}, nil
}
