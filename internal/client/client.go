// Package client is a Go client for the FeatureSteward REST API.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	// retryWait is the pause before the first retry; it doubles each time.
	retryWait time.Duration
}

// New returns a client for the API at baseURL (e.g. http://localhost:8080).
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},

		retryWait: 250 * time.Millisecond,
	}
}

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return e.Message
}

type User struct {
	Handle string `json:"handle"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

type Rule struct {
	Attribute string   `json:"attribute"`
	Values    []string `json:"values"`
	Serve     bool     `json:"serve"`
}

type EnvConfig struct {
	Enabled           bool   `json:"enabled"`
	RolloutPercentage int    `json:"rollout_percentage"`
	Rules             []Rule `json:"rules"`
}

type Flag struct {
	Key          string               `json:"key"`
	Name         string               `json:"name"`
	Description  string               `json:"description"`
	Steward      *string              `json:"steward"`
	CreatedAt    time.Time            `json:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
	ArchivedAt   *time.Time           `json:"archived_at"`
	Environments map[string]EnvConfig `json:"environments"`
}

type Environment struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
}

func (c *Client) Me(ctx context.Context) (User, error) {
	var u User
	err := c.do(ctx, http.MethodGet, "/api/v1/me", nil, &u)
	return u, err
}

// ListFlags lists active flags. steward filters by handle, or "none"
// for unassigned; "" lists all.
func (c *Client) ListFlags(ctx context.Context, steward string) ([]Flag, error) {
	path := "/api/v1/flags"
	if steward != "" {
		path += "?steward=" + url.QueryEscape(steward)
	}
	var out struct {
		Flags []Flag `json:"flags"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Flags, err
}

type Token struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	RevokedAt *time.Time `json:"revoked_at"`
}

func (c *Client) ListMyTokens(ctx context.Context) ([]Token, error) {
	var out struct {
		Tokens []Token `json:"tokens"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/me/tokens", nil, &out)
	return out.Tokens, err
}

func (c *Client) RevokeMyToken(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/me/tokens/%d", id), nil, nil)
}

func (c *Client) ListEnvironments(ctx context.Context) ([]Environment, error) {
	var out struct {
		Environments []Environment `json:"environments"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/environments", nil, &out)
	return out.Environments, err
}

// Flag returns one flag, including an archived one.
func (c *Client) Flag(ctx context.Context, key string) (Flag, error) {
	var f Flag
	err := c.do(ctx, http.MethodGet, "/api/v1/flags/"+url.PathEscape(key), nil, &f)
	return f, err
}

func (c *Client) CreateFlag(ctx context.Context, key, name, description, steward string) (Flag, error) {
	var f Flag
	body := map[string]string{"key": key, "name": name, "description": description, "steward": steward}
	err := c.do(ctx, http.MethodPost, "/api/v1/flags", body, &f)
	return f, err
}

// SetEnvironment replaces a flag's whole config in one environment.
func (c *Client) SetEnvironment(ctx context.Context, key, env string, cfg EnvConfig) (Flag, error) {
	if cfg.Rules == nil {
		cfg.Rules = []Rule{}
	}
	var f Flag
	err := c.do(ctx, http.MethodPut, "/api/v1/flags/"+url.PathEscape(key)+"/environments/"+url.PathEscape(env), cfg, &f)
	return f, err
}

func (c *Client) SetSteward(ctx context.Context, key, steward string) (Flag, error) {
	var f Flag
	err := c.do(ctx, http.MethodPut, "/api/v1/flags/"+url.PathEscape(key)+"/steward", map[string]string{"steward": steward}, &f)
	return f, err
}

func (c *Client) ArchiveFlag(ctx context.Context, key string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/flags/"+url.PathEscape(key), nil, nil)
}

// maxAttempts bounds how often a state-changing request is sent. Every
// attempt carries the same Idempotency-Key, so the server applies it once.
const maxAttempts = 3

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	idemKey := ""
	if method != http.MethodGet {
		idemKey = newKey()
	}
	wait := c.retryWait
	for attempt := 1; ; attempt++ {
		status, data, err := c.send(ctx, method, path, payload, idemKey)
		// GETs are safe to repeat too; they just don't need a key.
		retryable := err != nil && ctx.Err() == nil ||
			status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
		if !retryable || attempt == maxAttempts {
			if err != nil {
				return err
			}
			return decodeResponse(status, data, out)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
}

func (c *Client) send(ctx context.Context, method, path string, payload []byte, idemKey string) (int, []byte, error) {
	var r io.Reader
	if payload != nil {
		r = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	return resp.StatusCode, data, err
}

func decodeResponse(status int, data []byte, out any) error {
	if status >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &e)
		return &APIError{Status: status, Message: e.Error}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func newKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "stew-" + hex.EncodeToString(b)
}
