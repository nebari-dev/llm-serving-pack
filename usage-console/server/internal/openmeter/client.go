// Package openmeter serves the console's read API from OpenMeter: receipts
// from the usage events the gateway's collector sends, and spend from
// OpenMeter's meters and cost queries.
package openmeter

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/shared/openmeterapi"
)

// Client calls OpenMeter's HTTP API.
type Client struct {
	*openmeterapi.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{openmeterapi.New(baseURL, token, nil)}
}

// Event is one ingested CloudEvent.
type Event struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Subject string         `json:"subject"`
	Time    time.Time      `json:"time"`
	Data    map[string]any `json:"data"`
}

// eventQuery narrows GET /api/v2/events, which OpenMeter applies in
// ClickHouse: event times in [since, before) (a zero time leaves that end
// open), one event type, and event IDs. Empty fields don't narrow.
type eventQuery struct {
	since, before time.Time
	typ           string
	ids           []string
}

// filter is the query as the endpoint's filter parameter: a JSON object, one
// operator per field, so a window takes $and.
func (q eventQuery) filter() string {
	f := map[string]any{}
	var window []map[string]string
	if !q.since.IsZero() {
		window = append(window, map[string]string{"$gte": q.since.UTC().Format(time.RFC3339Nano)})
	}
	if !q.before.IsZero() {
		window = append(window, map[string]string{"$lt": q.before.UTC().Format(time.RFC3339Nano)})
	}
	switch len(window) {
	case 1:
		f["time"] = window[0]
	case 2:
		f["time"] = map[string]any{"$and": window}
	}
	if q.typ != "" {
		f["type"] = map[string]string{"$eq": q.typ}
	}
	if len(q.ids) > 0 {
		f["id"] = map[string][]string{"$in": q.ids}
	}
	if len(f) == 0 {
		return ""
	}
	b, _ := json.Marshal(f)
	return string(b)
}

// EachEvent calls fn with every event q matches, newest first, until fn
// returns false or the events run out.
func (c *Client) EachEvent(ctx context.Context, q eventQuery, fn func(Event) bool) error {
	cursor := ""
	filter := q.filter()
	for {
		v := url.Values{"limit": {"100"}}
		if cursor != "" {
			v.Set("cursor", cursor)
		}
		if filter != "" {
			v.Set("filter", filter)
		}
		var page struct {
			Items []struct {
				Event Event `json:"event"`
			} `json:"items"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.Get(ctx, "/api/v2/events", v, &page); err != nil {
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
