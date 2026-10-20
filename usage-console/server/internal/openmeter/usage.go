package openmeter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// dims are the event fields every usage query groups by; each view sums the
// cells it needs.
var dims = []string{"team", "user", "api_key", "model", "model_resource"}

// cell is the usage of one combination of dims in one window.
type cell struct {
	Start    time.Time
	Team     string
	User     string
	Key      string
	Model    string
	Resource string
	USD      float64
	// Unpriced: the cell's tokens have no price, so USD leaves them out.
	Unpriced bool
	Requests int
	Tokens   int
}

func (c cell) dim(name string) string {
	switch name {
	case "team":
		return c.Team
	case "user":
		return c.User
	case "api_key":
		return c.Key
	case "model":
		return c.Model
	case "model_resource":
		return c.Resource
	}
	return ""
}

// meterRow and costRow are OpenMeter's query results.
type meterRow struct {
	GroupBy     map[string]*string `json:"groupBy"`
	Value       float64            `json:"value"`
	WindowStart time.Time          `json:"windowStart"`
}

type costRow struct {
	Usage      string             `json:"usage"`
	Cost       *string            `json:"cost"`
	Dimensions map[string]*string `json:"dimensions"`
	From       time.Time          `json:"from"`
}

// window is an OpenMeter window size: meters take MINUTE, HOUR or DAY, cost
// queries the same as an ISO 8601 duration.
type window struct {
	meter, cost string
	size        time.Duration
}

var (
	byMinute = window{"MINUTE", "PT1M", time.Minute}
	byHour   = window{"HOUR", "PT1H", time.Hour}
	byDay    = window{"DAY", "P1D", 24 * time.Hour}
)

func (c *Client) meterQuery(ctx context.Context, slug string, from, to time.Time, w *window, groupBy []string) ([]meterRow, error) {
	q := url.Values{"from": {from.UTC().Format(time.RFC3339)}, "to": {to.UTC().Format(time.RFC3339)}, "groupBy": groupBy}
	if w != nil {
		q.Set("windowSize", w.meter)
	}
	var out struct {
		Data []meterRow `json:"data"`
	}
	return out.Data, c.get(ctx, "/api/v1/meters/"+slug+"/query", q, &out)
}

func (c *Client) costQuery(ctx context.Context, featureID string, from, to time.Time, w *window) ([]costRow, error) {
	body := map[string]any{"from": from.UTC().Format(time.RFC3339), "to": to.UTC().Format(time.RFC3339), "group_by_dimensions": dims}
	if w != nil {
		body["granularity"] = w.cost
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v3/openmeter/features/"+url.PathEscape(featureID)+"/cost/query", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Data []costRow `json:"data"`
	}
	return out.Data, c.do(req, &out)
}

// usage is [from, to) as cells, in windows of w (nil: one window). Cost and
// tokens come from each priced feature's cost query, requests from the
// llm_requests meter.
func (s *Server) usage(ctx context.Context, from, to time.Time, w *window) ([]cell, error) {
	fs, err := s.Client.Features(ctx)
	if err != nil {
		return nil, err
	}
	by := map[string]*cell{}
	var order []string
	at := func(start time.Time, g map[string]*string) *cell {
		if w == nil {
			start = from
		}
		v := func(k string) string {
			if p := g[k]; p != nil {
				return *p
			}
			return ""
		}
		c := cell{Start: start.UTC(), Team: v("team"), User: v("user"), Key: v("api_key"), Model: v("model"), Resource: v("model_resource")}
		k := c.Start.String()
		for _, d := range dims {
			k += "\x00" + c.dim(d)
		}
		if by[k] == nil {
			by[k] = &c
			order = append(order, k)
		}
		return by[k]
	}
	for _, f := range fs {
		if f.UnitCost == nil || f.UnitCost.Type != "llm" {
			continue
		}
		rows, err := s.Client.costQuery(ctx, f.ID, from, to, w)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			c := at(r.From, r.Dimensions)
			n, _ := strconv.ParseFloat(r.Usage, 64)
			c.Tokens += int(n)
			if r.Cost == nil {
				c.Unpriced = true
				continue
			}
			usd, _ := strconv.ParseFloat(*r.Cost, 64)
			c.USD += usd
		}
	}
	reqs, err := s.Client.meterQuery(ctx, "llm_requests", from, to, w, dims)
	if err != nil {
		return nil, err
	}
	for _, r := range reqs {
		at(r.WindowStart, r.GroupBy).Requests += int(r.Value)
	}
	out := make([]cell, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out, nil
}
