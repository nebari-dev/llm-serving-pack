// Package openmeterapi calls OpenMeter's HTTP API: one transport and list
// paging for every part of the pack that talks to OpenMeter (the key
// manager's customer sync, openmeter-setup, the usage console).
package openmeterapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is wrapped by Do when OpenMeter answers 404.
var ErrNotFound = errors.New("openmeter: not found")

// Client calls OpenMeter's API at one base URL.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New returns a Client for the API at baseURL. token is sent as a bearer
// token when set (OpenMeter itself has no authentication; a proxy in front
// of it may). A nil httpClient times requests out after 30 seconds.
func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: strings.TrimSuffix(baseURL, "/"), token: token, http: httpClient}
}

// Do sends method path?q with body as JSON (none when nil) and decodes the
// JSON answer into out (discarded when nil).
func (c *Client) Do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openmeter: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s %s", ErrNotFound, method, path)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("openmeter: %s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// Get is Do for a GET with no body.
func (c *Client) Get(ctx context.Context, path string, q url.Values, out any) error {
	return c.Do(ctx, http.MethodGet, path, q, nil, out)
}

// pageSize is how many items ListAll asks for a page.
const pageSize = 100

// ListAll reads every page of an OpenMeter v3 list endpoint (page[number]
// from 1, page[size]) with the query q, stopping at a short page or once
// meta.page.total items have been read.
func ListAll[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	var all []T
	for number := 1; ; number++ {
		var page struct {
			Data []T `json:"data"`
			Meta struct {
				Page struct {
					Total *int `json:"total"`
				} `json:"page"`
			} `json:"meta"`
		}
		pq := url.Values{"page[number]": {strconv.Itoa(number)}, "page[size]": {strconv.Itoa(pageSize)}}
		for k, v := range q {
			pq[k] = v
		}
		if err := c.Get(ctx, path, pq, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		total := page.Meta.Page.Total
		if len(page.Data) < pageSize || (total != nil && len(all) >= *total) {
			return all, nil
		}
	}
}
