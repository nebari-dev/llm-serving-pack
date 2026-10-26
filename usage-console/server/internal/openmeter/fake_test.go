package openmeter_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeOpenMeter answers the OpenMeter API calls the console API makes, from
// events and prices a test sets up. Pages of events are newest first, like
// OpenMeter's GET /api/v2/events.
type fakeOpenMeter struct {
	// mu guards events, for tests that add them while the API reads.
	mu        sync.Mutex
	events    []map[string]any
	overrides []map[string]any
	prices    []map[string]any
	customers []map[string]any
	// features replaces the default two price features when set.
	features []map[string]any
	pageSize int
	// eventReads counts GET /api/v2/events calls.
	eventReads int
	// cost is USD per token by model, for the fake's cost queries; a model
	// missing here has no price.
	cost map[string]float64
}

// request adds the events the usage collector sends for one gateway request:
// llm.request, then one llm.tokens per token type with a count.
func (f *fakeOpenMeter) request(id string, at time.Time, data map[string]any, tokens map[string]int) {
	f.events = append(f.events, event(id, "llm.request", at, data))
	for typ, n := range tokens {
		d := map[string]any{"token_type": typ, "tokens": n}
		for k, v := range data {
			switch k {
			case "access_path", "api_key", "model", "model_resource", "team", "user":
				d[k] = v
			}
		}
		f.events = append(f.events, event(id+":"+typ, "llm.tokens", at, d))
	}
}

func event(id, typ string, at time.Time, data map[string]any) map[string]any {
	return map[string]any{
		"id": id, "type": typ, "source": "nebari-llm-gateway", "subject": "sub-" + fmt.Sprint(data["user"]),
		"time": at.UTC().Format(time.RFC3339), "data": data,
	}
}

func (f *fakeOpenMeter) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/events", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.eventReads++
		evs := append([]map[string]any(nil), f.events...)
		f.mu.Unlock()
		sort.SliceStable(evs, func(i, j int) bool { return evs[i]["time"].(string) > evs[j]["time"].(string) })
		size := f.pageSize
		if size == 0 {
			size = 100
		}
		start := 0
		fmt.Sscan(r.URL.Query().Get("cursor"), &start)
		end := min(start+size, len(evs))
		items := []map[string]any{}
		for _, e := range evs[start:end] {
			items = append(items, map[string]any{"event": e})
		}
		out := map[string]any{"items": items}
		if end < len(evs) {
			out["nextCursor"] = fmt.Sprint(end)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /api/v3/openmeter/llm-cost/overrides", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, r, f.overrides)
	})
	mux.HandleFunc("GET /api/v3/openmeter/llm-cost/prices", func(w http.ResponseWriter, r *http.Request) {
		provider, model := r.URL.Query().Get("filter[provider][eq]"), r.URL.Query().Get("filter[model_id][eq]")
		out := []map[string]any{}
		for _, p := range f.prices {
			if p["provider"].(map[string]any)["id"] == provider && p["model"].(map[string]any)["id"] == model {
				out = append(out, p)
			}
		}
		writeJSON(w, map[string]any{"data": out})
	})
	mux.HandleFunc("GET /api/v3/openmeter/features", func(w http.ResponseWriter, r *http.Request) {
		fs := f.features
		if fs == nil {
			fs = []map[string]any{feature("f-mock", "mock-provider", "mock"), feature("f-bedrock", "bedrock", "bedrock")}
		}
		writePage(w, r, fs)
	})
	mux.HandleFunc("GET /api/v1/customers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"items": orEmpty(f.customers)})
	})
	mux.HandleFunc("GET /api/v1/meters/{slug}/query", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		typ := map[string]string{"llm_requests": "llm.request", "llm_tokens": "llm.tokens"}[r.PathValue("slug")]
		rows := f.aggregate(typ, q["groupBy"], q.Get("from"), q.Get("to"), q.Get("windowSize"), r.PathValue("slug") == "llm_tokens")
		out := []map[string]any{}
		for _, a := range rows {
			out = append(out, map[string]any{"groupBy": a.group, "value": a.value, "windowStart": a.start, "windowEnd": a.end})
		}
		writeJSON(w, map[string]any{"data": out})
	})
	mux.HandleFunc("POST /api/v3/openmeter/features/{id}/cost/query", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			From        string   `json:"from"`
			To          string   `json:"to"`
			Granularity string   `json:"granularity"`
			GroupBy     []string `json:"group_by_dimensions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		resource := map[string]string{"f-mock": "mock-provider", "f-bedrock": "bedrock"}[r.PathValue("id")]
		window := map[string]string{"PT1H": "HOUR", "P1D": "DAY"}[body.Granularity]
		// Price per model, then merge the models a query didn't group by. A
		// group with any unpriced model has no cost, like OpenMeter's.
		type row struct {
			dims        map[string]any
			usage, cost float64
			priced      bool
			start, end  string
		}
		merged := map[string]*row{}
		var order []string
		for _, a := range f.aggregate("llm.tokens", append(slices.Clone(body.GroupBy), "model"), body.From, body.To, window, true, "model_resource", resource) {
			dims := map[string]any{}
			k := a.start
			for _, name := range body.GroupBy {
				dims[name] = a.group[name]
				k += "|" + fmt.Sprint(a.group[name])
			}
			m := merged[k]
			if m == nil {
				m = &row{dims: dims, priced: true, start: a.start, end: a.end}
				merged[k] = m
				order = append(order, k)
			}
			rate, ok := f.cost[a.model]
			m.usage += a.value
			m.cost += rate * a.value
			m.priced = m.priced && ok
		}
		out := []map[string]any{}
		for _, k := range order {
			m := merged[k]
			r := map[string]any{"usage": fmt.Sprint(m.usage), "currency": "USD", "dimensions": m.dims, "from": m.start, "to": m.end}
			if m.priced {
				r["cost"] = fmt.Sprint(m.cost)
			}
			out = append(out, r)
		}
		writeJSON(w, map[string]any{"data": out})
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// feature is a priced OpenMeter feature over one model resource's tokens.
func feature(id, resource, provider string) map[string]any {
	return map[string]any{
		"id": id, "key": id,
		"meter":     map[string]any{"filters": map[string]any{"model_resource": map[string]any{"eq": resource}}},
		"unit_cost": map[string]any{"type": "llm", "provider": provider, "model_property": "model", "token_type_property": "token_type"},
	}
}

func price(provider, model string, from time.Time, rates map[string]string) map[string]any {
	return map[string]any{
		"provider": map[string]any{"id": provider}, "model": map[string]any{"id": model},
		"currency": "USD", "effective_from": from.UTC().Format(time.RFC3339), "pricing": rates,
	}
}

type agg struct {
	group      map[string]any
	model      string
	value      float64
	start, end string
}

// aggregate groups events of typ like OpenMeter's meters: by groupBy, within
// [from, to), in UTC windows of windowSize ("" for one window). where is an
// optional data field and value to filter on.
func (f *fakeOpenMeter) aggregate(typ string, groupBy []string, from, to, windowSize string, sum bool, where ...string) []*agg {
	parse := func(s string) time.Time {
		t, _ := time.Parse(time.RFC3339, s)
		return t
	}
	lo, hi := parse(from), parse(to)
	step := map[string]time.Duration{"HOUR": time.Hour, "DAY": 24 * time.Hour}[windowSize]
	by := map[string]*agg{}
	var keys []string
	for _, e := range f.events {
		d := e["data"].(map[string]any)
		at := parse(e["time"].(string))
		if e["type"] != typ || (!lo.IsZero() && at.Before(lo)) || (!hi.IsZero() && !at.Before(hi)) {
			continue
		}
		if len(where) == 2 && d[where[0]] != where[1] {
			continue
		}
		start, end := lo, hi
		if step > 0 {
			start = at.Truncate(step)
			end = start.Add(step)
		}
		g := map[string]any{}
		k := start.String()
		for _, name := range groupBy {
			g[name] = d[name]
			k += "|" + fmt.Sprint(d[name])
		}
		a := by[k]
		if a == nil {
			a = &agg{group: g, model: fmt.Sprint(d["model"]), start: start.Format(time.RFC3339), end: end.Format(time.RFC3339)}
			by[k] = a
			keys = append(keys, k)
		}
		if sum {
			a.value += float64(d["tokens"].(int))
		} else {
			a.value++
		}
	}
	out := []*agg{}
	for _, k := range keys {
		out = append(out, by[k])
	}
	return out
}

// writePage writes one page of items as OpenMeter's v3 lists do: page[size]
// a page (20 unless asked), page[number] from 1, and the total.
func writePage(w http.ResponseWriter, r *http.Request, items []map[string]any) {
	size, number := 20, 1
	if v, err := strconv.Atoi(r.URL.Query().Get("page[size]")); err == nil {
		size = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("page[number]")); err == nil {
		number = v
	}
	start := min((number-1)*size, len(items))
	end := min(start+size, len(items))
	writeJSON(w, map[string]any{"data": orEmpty(items[start:end]), "meta": map[string]any{"page": map[string]any{"number": number, "size": size, "total": len(items)}}})
}

func orEmpty(xs []map[string]any) []map[string]any {
	if xs == nil {
		return []map[string]any{}
	}
	return xs
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
