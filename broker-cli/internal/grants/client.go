// Package grants is the broker-cli's thin HTTP client for the broker's
// /api/grants endpoints. Types mirror mcp-broker/internal/grants wire
// shapes but are redeclared here to avoid a cross-module dependency.
package grants

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(dur)
	return nil
}

type Entry struct {
	Tool      string          `json:"tool"`
	ArgSchema json.RawMessage `json:"argSchema"`
}

type CreateRequest struct {
	Description string   `json:"description,omitempty"`
	TTL         Duration `json:"ttl"`
	Entries     []Entry  `json:"entries"`
}

type CreateResponse struct {
	ID          string    `json:"id"`
	Token       string    `json:"token"`
	Description string    `json:"description,omitempty"`
	Tools       []string  `json:"tools"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type Grant struct {
	ID          string     `json:"id"`
	Description string     `json:"description,omitempty"`
	Entries     []Entry    `json:"entries"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

func NewClient(endpoint, token string) *Client {
	return &Client{endpoint: endpoint, token: token, http: http.DefaultClient}
}

func (c *Client) Create(ctx context.Context, body CreateRequest) (*CreateResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/api/grants", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create grant: %s: %s", resp.Status, b)
	}
	var out CreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) List(ctx context.Context, includeInactive bool) ([]Grant, error) {
	u := c.endpoint + "/api/grants"
	if includeInactive {
		u += "?status=all"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list grants: %s: %s", resp.Status, b)
	}
	var out []Grant
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Revoke(ctx context.Context, id string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, c.endpoint+"/api/grants/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("revoke grant: %s: %s", resp.Status, b)
	}
	return nil
}
