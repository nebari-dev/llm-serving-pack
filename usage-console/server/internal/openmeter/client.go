// Package openmeter serves the console's read API from OpenMeter: receipts
// from the usage events the gateway's collector sends, and spend from
// OpenMeter's meters and cost queries.
package openmeter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls OpenMeter's HTTP API.
type Client struct {
	base  string
	token string
	http  *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{base: strings.TrimSuffix(baseURL, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

// Event is one ingested CloudEvent.
type Event struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Subject string         `json:"subject"`
	Time    time.Time      `json:"time"`
	Data    map[string]any `json:"data"`
}

// EachEvent calls fn with every event, newest first, until fn returns false
// or the events run out.
func (c *Client) EachEvent(ctx context.Context, fn func(Event) bool) error {
	cursor := ""
	for {
		q := url.Values{"limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Items []struct {
				Event Event `json:"event"`
			} `json:"items"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.get(ctx, "/api/v2/events", q, &page); err != nil {
			return err
		}
		for _, it := range page.Items {
			if !fn(it.Event) {
				return nil
			}
		}
		if page.NextCursor == "" || len(page.Items) == 0 {
			return nil
		}
		cursor = page.NextCursor
	}
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openmeter: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("openmeter: %s %s: %s", req.Method, req.URL.Path, res.Status)
	}
	return json.NewDecoder(res.Body).Decode(out)
}
