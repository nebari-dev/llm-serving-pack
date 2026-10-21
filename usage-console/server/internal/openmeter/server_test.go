package openmeter_test

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/api"
	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/model"
	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/openmeter"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func newAPI(t *testing.T, f *fakeOpenMeter) http.Handler {
	t.Helper()
	om := f.server(t)
	return (&openmeter.Server{Client: openmeter.NewClient(om.URL, ""), Tenant: "demo", Now: func() time.Time { return now }}).Handler()
}

func get[T any](t *testing.T, h http.Handler, path string) T {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/demo"+path, nil))
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET %s: %v in %s", path, err, rec.Body)
	}
	return out
}

func alice(model, key, path string) map[string]any {
	return map[string]any{
		"access_path": path, "api_key": key, "model": model, "model_resource": "mock-provider",
		"team": "team-a", "user": "alice", "response_code": "200", "response_flags": "", "usage_reported": "true", "duration_ms": 20,
	}
}

func TestReceiptsAreOnePerGatewayRequestNewestFirst(t *testing.T) {
	f := &fakeOpenMeter{}
	f.request("req-1", now.Add(-2*time.Minute), alice("mock/small", "key-a1", "api_key"), map[string]int{"input": 6, "cache_read": 5, "output": 6})
	f.request("req-2", now.Add(-time.Minute), alice("mock/large", "login", "jwt"), map[string]int{"input": 7, "output": 9})

	rs := get[[]model.Receipt](t, newAPI(t, f), "/receipts?limit=50")

	if len(rs) != 2 {
		t.Fatalf("got %d receipts, want 2: %+v", len(rs), rs)
	}
	r := rs[1]
	if rs[0].ID != "req-2" || r.ID != "req-1" {
		t.Fatalf("order %s, %s", rs[0].ID, r.ID)
	}
	if r.Project != "mock-provider" || r.ProjectID != "team-a/mock-provider" {
		t.Fatalf("project %q %q", r.Project, r.ProjectID)
	}
	if r.Team != "team-a" || r.Actor != "alice" || r.KeyID != "key-a1" || r.ResolvedModel != "mock/small" || r.RequestedModel != "mock/small" {
		t.Fatalf("identity %+v", r)
	}
	// OpenMeter's input count leaves out cached tokens; a receipt's includes them.
	if r.InputTokens != 11 || r.CachedInputTokens != 5 || r.OutputTokens != 6 {
		t.Fatalf("tokens in %d cached %d out %d", r.InputTokens, r.CachedInputTokens, r.OutputTokens)
	}
	if r.Status != 200 || r.Verdict != "allowed" || r.TS != now.Add(-2*time.Minute).UnixMilli() || r.DurationMS != 20 {
		t.Fatalf("status %d verdict %s ts %d duration %d", r.Status, r.Verdict, r.TS, r.DurationMS)
	}
}

// Receipts are priced the way OpenMeter's cost queries price usage, so they
// add up to the Spend page: a manual override beats the synced price, the
// newest price in effect now wins, and a token type with no rate leaves the
// request unpriced.
func TestReceiptCostMatchesOpenMeterPricing(t *testing.T) {
	f := &fakeOpenMeter{
		overrides: []map[string]any{
			price("mock", "mock/small", now.Add(-24*time.Hour), map[string]string{"input_per_token": "0.001", "cache_read_per_token": "0.0001", "output_per_token": "0.002"}),
		},
		prices: []map[string]any{
			price("mock", "mock/small", now.Add(-48*time.Hour), map[string]string{"input_per_token": "9", "output_per_token": "9"}),
			price("bedrock", "amazon.nova-2-lite", now.Add(-48*time.Hour), map[string]string{"input_per_token": "0.00000033", "output_per_token": "0.00000275"}),
			price("bedrock", "amazon.nova-2-lite", now.Add(-24*time.Hour), map[string]string{"input_per_token": "0.00000066", "output_per_token": "0.0000055"}),
			price("bedrock", "amazon.nova-2-lite", now.Add(time.Hour), map[string]string{"input_per_token": "1", "output_per_token": "1"}),
		},
	}
	f.request("req-1", now.Add(-2*time.Minute), alice("mock/small", "key-a1", "api_key"), map[string]int{"input": 6, "cache_read": 5, "output": 6})
	nova := alice("amazon.nova-2-lite", "key-a2", "api_key")
	nova["model_resource"] = "bedrock"
	f.request("req-2", now.Add(-time.Minute), nova, map[string]int{"input": 1000, "output": 100})
	unknown := alice("mock/unpriced", "key-a1", "api_key")
	f.request("req-3", now.Add(-30*time.Second), unknown, map[string]int{"input": 3})
	// Nova's price has no cache rate.
	f.request("req-4", now.Add(-20*time.Second), nova, map[string]int{"input": 10, "cache_read": 4})

	rs := get[[]model.Receipt](t, newAPI(t, f), "/receipts")
	cost := map[string]*float64{}
	for _, r := range rs {
		cost[r.ID] = r.CostUSD
	}
	near := func(id string, want float64) {
		t.Helper()
		if cost[id] == nil || *cost[id] < want-1e-12 || *cost[id] > want+1e-12 {
			t.Fatalf("%s cost %v, want %v", id, cost[id], want)
		}
	}
	near("req-1", 6*0.001+5*0.0001+6*0.002)
	near("req-2", 1000*0.00000066+100*0.0000055)
	for _, id := range []string{"req-3", "req-4"} {
		if cost[id] != nil {
			t.Fatalf("%s costs %v, want unknown (null)", id, *cost[id])
		}
	}
}

// The gateway sends no token counts for a stream the caller cancelled. The
// request still shows, marked truncated, with an unknown cost.
func TestCancelledStreamIsATruncatedReceipt(t *testing.T) {
	f := &fakeOpenMeter{overrides: []map[string]any{
		price("mock", "mock/large", now.Add(-time.Hour), map[string]string{"input_per_token": "0.003", "output_per_token": "0.015"}),
	}}
	d := alice("mock/large", "key-a1", "api_key")
	d["usage_reported"], d["response_flags"] = "false", "DR"
	f.request("req-1", now.Add(-time.Minute), d, nil)

	rs := get[[]model.Receipt](t, newAPI(t, f), "/receipts")

	if len(rs) != 1 || rs[0].Verdict != "truncated" || rs[0].CostUSD != nil || rs[0].InputTokens != 0 {
		t.Fatalf("got %+v", rs)
	}
	if rs[0].ErrorCode == "" {
		t.Fatalf("no reason given: %+v", rs[0])
	}
}

func bob(model, key string) map[string]any {
	d := alice(model, key, "api_key")
	d["team"], d["user"] = "team-b", "bob"
	return d
}

// Spend groups usage the way the console's Spend page asks: the range, the
// same span before it for the deltas, biggest spend first.
func TestSpendByTeamComparesWithThePreviousRange(t *testing.T) {
	f := &fakeOpenMeter{cost: map[string]float64{"mock/small": 0.001, "mock/large": 0.01}}
	f.request("a1", now.Add(-3*time.Hour), alice("mock/small", "key-a1", "api_key"), map[string]int{"input": 10, "output": 10})
	f.request("a2", now.Add(-2*time.Hour), alice("mock/small", "login", "jwt"), map[string]int{"input": 30})
	f.request("a0", now.Add(-30*time.Hour), alice("mock/small", "key-a1", "api_key"), map[string]int{"input": 100})
	f.request("b1", now.Add(-time.Hour), bob("mock/large", "key-b1"), map[string]int{"input": 5, "output": 5})
	f.request("b2", now.Add(-time.Hour), bob("mock/unpriced", "key-b1"), map[string]int{"input": 7})

	v := get[api.SpendView](t, newAPI(t, f), "/spend?range=24h&by=team")

	if v.By != "team" || v.To != now.UnixMilli() || v.From != now.Add(-24*time.Hour).UnixMilli() {
		t.Fatalf("window %+v", v)
	}
	if len(v.Rows) != 2 {
		t.Fatalf("rows %+v", v.Rows)
	}
	b, a := v.Rows[0], v.Rows[1]
	if b.ID != "team-b" || b.SpendUSD != 0.1 || b.Requests != 2 || b.Tokens != 17 || b.Unpriced != 1 {
		t.Fatalf("team-b %+v", b)
	}
	if a.ID != "team-a" || a.SpendUSD != 0.05 || a.PrevSpendUSD != 0.1 || a.Requests != 2 || a.Tokens != 50 || a.Unpriced != 0 {
		t.Fatalf("team-a %+v", a)
	}
	if v.Unpriced != 1 {
		t.Fatalf("unpriced %d", v.Unpriced)
	}
}

// The Spend chart: one bar per hour over 24h, split by the dimension, and the
// month-end projection from month-to-date plus the trailing daily average.
func TestSpendTrendAndMonthProjection(t *testing.T) {
	f := &fakeOpenMeter{cost: map[string]float64{"mock/small": 0.001}}
	f.request("a1", now.Add(-150*time.Minute), alice("mock/small", "key-a1", "api_key"), map[string]int{"input": 100})
	f.request("b1", now.Add(-90*time.Minute), bob("mock/small", "key-b1"), map[string]int{"input": 200})
	// Earlier this month, outside the 24h range.
	f.request("a0", now.Add(-5*24*time.Hour), alice("mock/small", "key-a1", "api_key"), map[string]int{"input": 700})

	v := get[api.SpendView](t, newAPI(t, f), "/spend?range=24h&by=team")

	tr := v.Trend
	if tr.BucketMS != time.Hour.Milliseconds() || len(tr.Points) != 24 {
		t.Fatalf("buckets %d x %d", len(tr.Points), tr.BucketMS)
	}
	last := tr.Points[len(tr.Points)-1]
	if last.T != now.Truncate(time.Hour).UnixMilli() {
		t.Fatalf("last bucket %d", last.T)
	}
	// 09:30 and 10:30 land in the buckets at 09:00 and 10:00.
	at := func(h int) map[string]float64 { return tr.Points[len(tr.Points)-1-h].Values }
	if at(3)["team-a"] != 0.1 || at(2)["team-b"] != 0.2 {
		t.Fatalf("points %+v %+v", at(3), at(2))
	}
	if len(tr.Order) != 2 || tr.Order[0] != "team-a" {
		t.Fatalf("order %v (30-day spend: team-a 0.8, team-b 0.2)", tr.Order)
	}
	p := v.Period
	if p.MonthToDateUSD != 1.0 || p.TrailingDays != 7 || math.Abs(p.TrailingDailyUSD-1.0/7) > 1e-9 {
		t.Fatalf("period %+v", p)
	}
	if math.Abs(p.ProjectedUSD-(1.0+p.TrailingDailyUSD*p.RemainingDays)) > 1e-9 {
		t.Fatalf("projected %+v", p)
	}
}

// Every call the console makes when it loads answers, so it opens. What this
// pack has no data for (routing, guardrails, budgets) is empty.
func TestConsoleStartupCallsAnswer(t *testing.T) {
	f := &fakeOpenMeter{cost: map[string]float64{"mock/small": 0.001}}
	f.request("a1", now.Add(-90*time.Minute), alice("mock/small", "key-a1", "api_key"), map[string]int{"input": 100})
	f.request("a2", now.Add(-80*time.Minute), alice("mock/small", "login", "jwt"), map[string]int{"input": 10})
	f.request("b1", now.Add(-50*time.Hour), bob("mock/small", "key-b1"), map[string]int{"input": 200})
	h := newAPI(t, f)

	for _, p := range []string{"/models", "/backends", "/routes", "/budgets", "/policies", "/changes", "/receipts?limit=240"} {
		get[[]map[string]any](t, h, p)
	}

	teams := get[[]model.Team](t, h, "/teams")
	if len(teams) != 2 || teams[0].ID != "team-a" || teams[1].ID != "team-b" {
		t.Fatalf("teams %+v", teams)
	}
	projects := get[[]model.Project](t, h, "/projects")
	if len(projects) != 2 || projects[0].Team != "team-a" || projects[0].Name != "mock-provider" {
		t.Fatalf("projects %+v", projects)
	}

	// Keys are the API keys seen in the last 30 days; a Keycloak login isn't one.
	keys := get[[]map[string]any](t, h, "/keys")
	if len(keys) != 2 {
		t.Fatalf("keys %+v", keys)
	}
	k := keys[0]
	if k["id"] != "key-a1" || k["owner"] != "alice" || k["team"] != "team-a" || k["project"] != "mock-provider" || k["status"] != "active" {
		t.Fatalf("key %+v", k)
	}
	if k["requests24h"] != 1.0 || k["spend24hUsd"] != 0.1 || len(k["hourly24h"].([]any)) != 24 || k["lastUsedAt"] != float64(now.Add(-90*time.Minute).Truncate(time.Hour).UnixMilli()) {
		t.Fatalf("key usage %+v", k)
	}
	if keys[1]["id"] != "key-b1" || keys[1]["requests24h"] != 0.0 {
		t.Fatalf("idle key %+v", keys[1])
	}

	traffic := get[[]model.SeriesPoint](t, h, "/series/traffic?range=24h")
	sum := 0
	for _, p := range traffic {
		sum += p.Allowed
	}
	if len(traffic) != 48 || sum != 2 {
		t.Fatalf("traffic %d points, %d allowed", len(traffic), sum)
	}
	days := get[[]model.SpendPoint](t, h, "/series/spend?days=30")
	if len(days) != 30 || days[29].Day != "10-20" || days[29].ByTeam["team-a"] != 0.11 || days[27].ByTeam["team-b"] != 0.2 {
		t.Fatalf("spend series %+v", days[27:])
	}
}

// The console is read-only: whoever reaches it may read, and every write is
// disabled with the reason.
func TestSessionIsReadOnly(t *testing.T) {
	s := get[api.Session](t, newAPI(t, &fakeOpenMeter{}), "/session")

	if s.Tenant.ID != "demo" || len(s.Actor.Roles) == 0 || s.Actor.Role != "viewer" {
		t.Fatalf("session %+v", s)
	}
	if !s.Permissions[api.ActRead].Allowed {
		t.Fatalf("read not allowed: %+v", s.Permissions)
	}
	for _, a := range []api.Action{api.ActBudgets, api.ActRouting, api.ActOwnKeys, api.ActPrices, api.ActPublishRules} {
		if p, ok := s.Permissions[a]; !ok || p.Allowed {
			t.Fatalf("%s: %+v (present %v)", a, p, ok)
		}
	}
	if s.Warden != nil {
		t.Fatalf("warden %+v", s.Warden)
	}
}

// Overview's numbers: requests and spend for the range against the span
// before, and the team whose spend rose most.
func TestSummaryComparesRanges(t *testing.T) {
	f := &fakeOpenMeter{cost: map[string]float64{"mock/small": 0.001}}
	f.request("a1", now.Add(-time.Hour), alice("mock/small", "key-a1", "api_key"), map[string]int{"input": 100})
	f.request("b1", now.Add(-2*time.Hour), bob("mock/small", "key-b1"), map[string]int{"input": 400})
	f.request("b0", now.Add(-30*time.Hour), bob("mock/small", "key-b1"), map[string]int{"input": 100})
	f.request("a0", now.Add(-31*time.Hour), alice("mock/small", "key-a1", "api_key"), map[string]int{"input": 100})

	s := get[api.Summary](t, newAPI(t, f), "/summary?range=24h")

	if s.Current.Requests != 2 || s.Current.SpendUSD != 0.5 || s.Previous.Requests != 2 || s.Previous.SpendUSD != 0.2 {
		t.Fatalf("totals %+v / %+v", s.Current, s.Previous)
	}
	if s.TopTeamIncrease == nil || s.TopTeamIncrease.Team != "team-b" || s.TopTeamIncrease.DeltaUSD != 0.3 {
		t.Fatalf("top increase %+v", s.TopTeamIncrease)
	}
}

// Spend by key keeps each person's Keycloak-login usage apart: "login" is
// not one shared key.
func TestSpendByKeySplitsLoginUsageByPerson(t *testing.T) {
	f := &fakeOpenMeter{cost: map[string]float64{"mock/small": 0.001}}
	f.request("a1", now.Add(-time.Hour), alice("mock/small", "login", "jwt"), map[string]int{"input": 100})
	b := bob("mock/small", "login")
	b["access_path"] = "jwt"
	f.request("b1", now.Add(-time.Hour), b, map[string]int{"input": 300})

	v := get[api.SpendView](t, newAPI(t, f), "/spend?range=24h&by=key")

	if len(v.Rows) != 2 || v.Rows[0].ID != "login:bob" || v.Rows[0].Label != "bob (Keycloak login)" || v.Rows[1].ID != "login:alice" || !v.Rows[0].NoDrill {
		t.Fatalf("rows %+v", v.Rows)
	}
}

// The calls pages make after loading answer too: the receipt count from
// OpenMeter, and empty answers where this pack keeps nothing (configuration
// history, savings, gateway timing).
func TestPageCallsAnswer(t *testing.T) {
	f := &fakeOpenMeter{}
	f.request("a1", now.Add(-2*time.Hour), alice("mock/small", "key-a1", "api_key"), nil)
	f.request("a2", now.Add(-time.Hour), alice("mock/small", "key-a1", "api_key"), nil)
	f.request("a3", now.Add(-26*time.Hour), alice("mock/small", "key-a1", "api_key"), nil)
	h := newAPI(t, f)

	since := now.Add(-24 * time.Hour).UnixMilli()
	c := get[api.ReceiptCount](t, h, "/receipts/count?since="+strconv.FormatInt(since, 10))
	if c.Count == nil || *c.Count != 2 || c.Since != since {
		t.Fatalf("count %+v", c)
	}
	a := get[api.ActivityView](t, h, "/activity?range=7d")
	if a.Changes == nil || a.Events == nil || a.Until != now.UnixMilli() {
		t.Fatalf("activity %+v", a)
	}
	sv := get[api.SavingsView](t, h, "/spend/savings")
	if sv.Opportunities == nil {
		t.Fatalf("savings %+v", sv)
	}
	o := get[map[string]any](t, h, "/gateway/overhead")
	if o["samples"] != 0.0 {
		t.Fatalf("overhead %+v", o)
	}
}

// Live Traffic: requests that reach OpenMeter after the page opened arrive
// on the stream, and arrive again when their token counts land later.
func TestStreamSendsNewAndUpdatedReceipts(t *testing.T) {
	f := &fakeOpenMeter{}
	f.request("old", now.Add(-time.Hour), alice("mock/small", "key-a1", "api_key"), map[string]int{"input": 1})
	om := f.server(t)
	srv := httptest.NewServer((&openmeter.Server{Client: openmeter.NewClient(om.URL, ""), Tenant: "demo", Now: func() time.Time { return now }, Poll: 20 * time.Millisecond}).Handler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/demo/stream/traffic", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	events := make(chan model.Receipt)
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if sc.Text() == ": ready" {
				close(ready)
			}
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var r model.Receipt
				if json.Unmarshal([]byte(data), &r) == nil && r.ID != "" {
					events <- r
				}
			}
		}
	}()
	next := func() model.Receipt {
		t.Helper()
		select {
		case r := <-events:
			return r
		case <-ctx.Done():
			t.Fatal("no receipt on the stream")
		}
		return model.Receipt{}
	}

	// What was already there when the stream opened isn't sent again.
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("stream never ready")
	}
	f.mu.Lock()
	f.events = append(f.events, event("new", "llm.request", now, alice("mock/small", "key-a1", "api_key")))
	f.mu.Unlock()
	if r := next(); r.ID != "new" || r.InputTokens != 0 {
		t.Fatalf("first %+v", r)
	}
	f.mu.Lock()
	f.events = append(f.events, event("new:input", "llm.tokens", now, map[string]any{"token_type": "input", "tokens": 9, "user": "alice"}))
	f.mu.Unlock()
	if r := next(); r.ID != "new" || r.InputTokens != 9 {
		t.Fatalf("update %+v", r)
	}
}

// Provider is the OpenMeter provider of each model resource's price feature.
func TestSpendByProvider(t *testing.T) {
	f := &fakeOpenMeter{cost: map[string]float64{"mock/small": 0.001, "amazon.nova-2-lite": 0.0001}}
	f.request("a1", now.Add(-time.Hour), alice("mock/small", "key-a1", "api_key"), map[string]int{"input": 100})
	nova := alice("amazon.nova-2-lite", "key-a2", "api_key")
	nova["model_resource"] = "bedrock"
	f.request("a2", now.Add(-time.Hour), nova, map[string]int{"input": 100})

	v := get[api.SpendView](t, newAPI(t, f), "/spend?range=24h&by=provider")

	if v.By != "provider" || len(v.Rows) != 2 || v.Rows[0].ID != "mock" || v.Rows[1].ID != "bedrock" || v.Rows[1].SpendUSD != 0.01 {
		t.Fatalf("rows %+v", v.Rows)
	}
}

// Gateway model IDs are priced under OpenMeter's canonical IDs: Bedrock's
// region prefix and version suffix go, and a dot between digits becomes a
// hyphen, as OpenMeter's cost queries look them up.
func TestReceiptCostFindsPricesUnderOpenMeterModelIDs(t *testing.T) {
	f := &fakeOpenMeter{prices: []map[string]any{
		price("bedrock", "amazon.nova-2-lite", now.Add(-time.Hour), map[string]string{"input_per_token": "0.000001", "output_per_token": "0.000002"}),
		price("bedrock", "xai.grok-4-6", now.Add(-time.Hour), map[string]string{"input_per_token": "0.00001", "output_per_token": "0.00002"}),
	}}
	for id, m := range map[string]string{"r1": "us.amazon.nova-2-lite-v1:0", "r2": "us.xai.grok-4.6"} {
		d := alice(m, "key-a1", "api_key")
		d["model_resource"] = "bedrock"
		f.request(id, now.Add(-time.Minute), d, map[string]int{"input": 1000, "output": 100})
	}

	rs := get[[]model.Receipt](t, newAPI(t, f), "/receipts")

	cost := map[string]float64{}
	for _, r := range rs {
		if r.CostUSD == nil {
			t.Fatalf("%s (%s) unpriced", r.ID, r.ResolvedModel)
		}
		cost[r.ID] = *r.CostUSD
	}
	if cost["r1"] != 0.0012 || cost["r2"] != 0.012 {
		t.Fatalf("costs %v", cost)
	}
}

// A request the gateway refused after identifying the caller (a key not yet
// allowed on the model, a rate limit) has no tokens because it never reached
// the model: it is blocked or throttled, not a truncated stream.
func TestRefusedRequestsAreBlockedOrThrottled(t *testing.T) {
	f := &fakeOpenMeter{}
	for id, code := range map[string]string{"r403": "403", "r429": "429"} {
		d := alice("mock/small", "key-a1", "api_key")
		d["response_code"], d["usage_reported"] = code, "false"
		f.request(id, now.Add(-time.Minute), d, nil)
	}

	rs := get[[]model.Receipt](t, newAPI(t, f), "/receipts")

	verdict := map[string]string{}
	for _, r := range rs {
		verdict[r.ID] = r.Verdict
	}
	if verdict["r403"] != "blocked" || verdict["r429"] != "throttled" {
		t.Fatalf("verdicts %v", verdict)
	}
}
