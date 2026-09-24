// Package client is a Go client for the FeatureSteward REST API.
package client

import (
	"bytes"
	"context"
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
}

// New returns a client for the API at baseURL (e.g. http://localhost:8080).
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
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

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &e)
		return &APIError{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}
