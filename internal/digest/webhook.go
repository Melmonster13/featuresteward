package digest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Webhook posts messages as {"text": ...}, which Slack incoming webhooks
// and many chat tools accept. Its URL is a secret: errors never include it.
type Webhook struct {
	url    string
	client *http.Client
}

// NewWebhook checks rawURL: https, or http only to this machine.
func NewWebhook(rawURL string) (*Webhook, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("STALE_WEBHOOK_URL must be an https:// URL")
	}
	if u.Scheme == "http" {
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
		default:
			return nil, errors.New("STALE_WEBHOOK_URL must use https, except to localhost")
		}
	}
	return &Webhook{url: rawURL, client: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (w *Webhook) Send(ctx context.Context, text string) error {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return errors.New("stale webhook: bad request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		// url.Error repeats the URL, which holds the webhook's secret.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("stale webhook: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("stale webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}
