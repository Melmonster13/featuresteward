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
	// PermanentReason is set when the flag is meant to last.
	PermanentReason *string             `json:"permanent_reason"`
	Activity        map[string]Activity `json:"activity"`
	// Stale is set when the flag looks safe to remove.
	Stale *Staleness `json:"stale"`
}

type Activity struct {
	ChangedAt   time.Time `json:"changed_at"`
	EvaluatedAt time.Time `json:"evaluated_at"`
}

// Staleness says why a flag looks safe to remove.
type Staleness struct {
	Reason     string    `json:"reason"` // unused, always_on, always_off, settled_mixed
	Since      time.Time `json:"since"`
	Suggestion string    `json:"suggestion"`
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
// for unassigned; "" lists all. staleOnly lists only stale flags.
func (c *Client) ListFlags(ctx context.Context, steward string, staleOnly bool) ([]Flag, error) {
	q := url.Values{}
	if steward != "" {
		q.Set("steward", steward)
	}
	if staleOnly {
		q.Set("stale", "true")
	}
	path := "/api/v1/flags"
	if len(q) > 0 {
		path += "?" + q.Encode()
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

// SetEnvironment replaces a flag's whole config in one environment. In a
// protected environment, a reason makes it an admin's emergency change.
func (c *Client) SetEnvironment(ctx context.Context, key, env string, cfg EnvConfig, reason string) (Flag, error) {
	var f Flag
	err := c.do(ctx, http.MethodPut, envPath(key, env), withReason{normalize(cfg), reason}, &f)
	return f, err
}

// ChangeRequest proposes a config for a flag in a protected environment.
type ChangeRequest struct {
	ID            int64      `json:"id"`
	Flag          string     `json:"flag"`
	Environment   string     `json:"environment"`
	RequestedBy   string     `json:"requested_by"`
	Reason        string     `json:"reason"`
	Base          EnvConfig  `json:"base"`
	Proposed      EnvConfig  `json:"proposed"`
	Status        string     `json:"status"`
	ReviewedBy    *string    `json:"reviewed_by"`
	ReviewComment string     `json:"review_comment"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
}

// RequestChange asks for cfg in a protected environment; a reviewer
// applies it by approving.
func (c *Client) RequestChange(ctx context.Context, key, env string, cfg EnvConfig, reason string) (ChangeRequest, error) {
	var r ChangeRequest
	err := c.do(ctx, http.MethodPost, envPath(key, env)+"/requests", withReason{normalize(cfg), reason}, &r)
	return r, err
}

// Requests lists change requests, newest first. status and flag filter
// when set.
func (c *Client) Requests(ctx context.Context, status, flag string) ([]ChangeRequest, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if flag != "" {
		q.Set("flag", flag)
	}
	path := "/api/v1/requests"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Requests []ChangeRequest `json:"requests"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Requests, err
}

func (c *Client) Approve(ctx context.Context, id int64, comment string) (ChangeRequest, error) {
	return c.review(ctx, id, "approve", comment)
}

func (c *Client) Reject(ctx context.Context, id int64, comment string) (ChangeRequest, error) {
	return c.review(ctx, id, "reject", comment)
}

func (c *Client) Cancel(ctx context.Context, id int64) (ChangeRequest, error) {
	var r ChangeRequest
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/requests/%d/cancel", id), nil, &r)
	return r, err
}

func (c *Client) review(ctx context.Context, id int64, action, comment string) (ChangeRequest, error) {
	var r ChangeRequest
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/requests/%d/%s", id, action), map[string]string{"comment": comment}, &r)
	return r, err
}

// withReason is an EnvConfig plus the optional reason the API accepts.
type withReason struct {
	EnvConfig
	Reason string `json:"reason,omitempty"`
}

func envPath(key, env string) string {
	return "/api/v1/flags/" + url.PathEscape(key) + "/environments/" + url.PathEscape(env)
}

func normalize(cfg EnvConfig) EnvConfig {
	if cfg.Rules == nil {
		cfg.Rules = []Rule{}
	}
	return cfg
}

func (c *Client) SetSteward(ctx context.Context, key, steward string) (Flag, error) {
	var f Flag
	err := c.do(ctx, http.MethodPut, "/api/v1/flags/"+url.PathEscape(key)+"/steward", map[string]string{"steward": steward}, &f)
	return f, err
}

// SetPermanent marks a flag as meant to last, or clears the mark when
// reason is "".
func (c *Client) SetPermanent(ctx context.Context, key, reason string) (Flag, error) {
	var f Flag
	err := c.do(ctx, http.MethodPut, "/api/v1/flags/"+url.PathEscape(key)+"/permanent", map[string]string{"reason": reason}, &f)
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
