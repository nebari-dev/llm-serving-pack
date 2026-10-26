// Package openmeter sets up what the LLM serving pack needs in OpenMeter:
// a priced feature per model resource, and manual prices.
package openmeter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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

// pageSize is how many items listAll asks OpenMeter for a page.
const pageSize = 100

// listAll reads every page of an OpenMeter list endpoint (page[number] from
// 1, page[size]), stopping at a short page or once meta.page.total items
// have been read.
func listAll[T any](ctx context.Context, c *Client, path string) ([]T, error) {
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
		q := url.Values{"page[number]": {strconv.Itoa(number)}, "page[size]": {strconv.Itoa(pageSize)}}
		if err := c.get(ctx, path, q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		total := page.Meta.Page.Total
		if len(page.Data) < pageSize || (total != nil && len(all) >= *total) {
			return all, nil
		}
	}
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
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("openmeter: %s %s: %s", req.Method, req.URL.Path, res.Status)
	}
	return json.NewDecoder(res.Body).Decode(out)
}
