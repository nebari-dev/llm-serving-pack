package api

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// §7.5.9 Activity: config changes and traffic on one timeline. Everything
// here reads receipts_5m, so a change's before/after and the traffic events
// line up with the counts Traffic and Overview show.

const (
	bucket5m = 5 * time.Minute
	// impactBuckets either side of a change: an hour of 5-minute buckets.
	impactBuckets = 12
	// minCompare is the fewest requests either side for a comparison to mean
	// anything; Overview's featured change uses the same floor.
	minCompare = 20
	// A metric has moved when it changed by this share of its before value.
	moveShare = 0.05
	// Backend events use the banner's rule over 15 minutes of buckets.
	backendWindowBuckets = int(degradationWindow / bucket5m)
)

// budgetLevels are the shares of a cap that make a traffic event.
var budgetLevels = []float64{0.8, 1}

// Agg is tenant traffic over one side of a change.
type Agg struct {
	Requests             int     `json:"requests"`
	CostPerRequestUSD    float64 `json:"costPerRequestUsd"`    // over served requests
	ErrorRate            float64 `json:"errorRate"`            // 5xx and 429, share 0–1
	BlockedRedactedShare float64 `json:"blockedRedactedShare"` // share 0–1
}

// ActivityImpact compares the buckets either side of a change. The change's
// own bucket counts as after, so up to 5 minutes before it land there too.
type ActivityImpact struct {
	WindowMinutes int   `json:"windowMinutes"`
	Pivot         int64 `json:"pivot"` // start of the change's bucket, epoch ms
	Before        Agg   `json:"before"`
	After         Agg   `json:"after"`
	// Bins are requests per 5-minute bucket; the first Split are before.
	Bins       []int  `json:"bins"`
	Split      int    `json:"split"`
	Comparable bool   `json:"comparable"`
	Effect     string `json:"-"`
	EffectTone string `json:"-"`
}

// ActivityChange is an audit row with its computed effect in place of the
// stored one.
type ActivityChange struct {
	model.Change
	Impact ActivityImpact `json:"impact"`
}

// TrafficEvent is something traffic did on its own: a backend started or
// stopped failing, or a budget crossed a share of its cap.
type TrafficEvent struct {
	ID     string `json:"id"`
	TS     int64  `json:"ts"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Tone   string `json:"tone"`
	To     string `json:"to"`
}

type ActivityView struct {
	Since   int64            `json:"since"`
	Until   int64            `json:"until"`
	Changes []ActivityChange `json:"changes"`
	Events  []TrafficEvent   `json:"events"`
}

type tally struct {
	requests, served, errors, blockedRedacted int
	cost                                      float64
}

func (a *tally) add(b store.ActivityBucket) {
	a.merge(tally{b.Requests, b.Served, b.Errors, b.BlockedRedacted, b.CostUSD})
}

func (a *tally) merge(b tally) {
	a.requests += b.requests
	a.served += b.served
	a.errors += b.errors
	a.blockedRedacted += b.blockedRedacted
	a.cost += b.cost
}

func (a tally) agg() Agg {
	g := Agg{Requests: a.requests}
	if a.served > 0 {
		g.CostPerRequestUSD = a.cost / float64(a.served)
	}
	if a.requests > 0 {
		g.ErrorRate = float64(a.errors) / float64(a.requests)
		g.BlockedRedactedShare = float64(a.blockedRedacted) / float64(a.requests)
	}
	return g
}

// impactAt compares up to an hour of buckets either side of at. The after
// side takes only complete buckets, since the one still filling would read
// as a drop in requests, and the before side matches its length.
func impactAt(bs []store.ActivityBucket, at, now time.Time) ActivityImpact {
	pivot := at.Truncate(bucket5m)
	n := 0
	if now.After(pivot) {
		n = min(impactBuckets, int(now.Sub(pivot)/bucket5m))
	}
	byStart := map[int64]*tally{}
	for _, b := range bs {
		k := b.Start.UnixMilli()
		if byStart[k] == nil {
			byStart[k] = &tally{}
		}
		byStart[k].add(b)
	}
	im := ActivityImpact{WindowMinutes: n * 5, Pivot: pivot.UnixMilli(), Bins: make([]int, 2*n), Split: n}
	var before, after tally
	for i := range 2 * n {
		c := byStart[pivot.Add(time.Duration(i-n)*bucket5m).UnixMilli()]
		if c == nil {
			continue
		}
		im.Bins[i] = c.requests
		side := &after
		if i < n {
			side = &before
		}
		side.merge(*c)
	}
	im.Before, im.After = before.agg(), after.agg()
	im.EffectTone, im.Effect, im.Comparable = effectOf(im.Before, im.After)
	return im
}

// effectOf states what moved. Every metric is better lower, so any metric
// up by 5% or more is a regression, and an improvement is something down by
// that much with nothing up. A metric that was 0 before has no share to move
// by, so it isn't counted.
func effectOf(before, after Agg) (tone, text string, comparable bool) {
	if before.Requests < minCompare || after.Requests < minCompare {
		return "neutral", fmt.Sprintf("Too little traffic to compare (%d before, %d after).", before.Requests, after.Requests), false
	}
	metrics := []struct {
		label string
		b, a  float64
	}{
		{"cost/request", before.CostPerRequestUSD, after.CostPerRequestUSD},
		{"error rate", before.ErrorRate, after.ErrorRate},
		{"blocked + redacted", before.BlockedRedactedShare, after.BlockedRedactedShare},
	}
	var moved []string
	worse := false
	for _, m := range metrics {
		if m.b <= 0 {
			continue
		}
		d := (m.a - m.b) / m.b
		if math.Abs(d) < moveShare {
			continue
		}
		sign := "−"
		if d > 0 {
			sign, worse = "+", true
		}
		moved = append(moved, fmt.Sprintf("%s %s%d%%", m.label, sign, int(math.Round(math.Abs(d)*100))))
	}
	switch {
	case len(moved) == 0:
		return "neutral", "Nothing moved by 5% or more.", true
	case worse:
		return "bad", strings.Join(moved, ", "), true
	default:
		return "good", strings.Join(moved, ", "), true
	}
}

// backendEvents walks each backend's buckets and reports when it crossed
// the banner's failing threshold (backendFailing), either way. A failing episode only ends
// once the backend has stayed under the threshold for a full window, so thin
// traffic dipping in and out reads as one episode; the recovery is pinned to
// where that stretch began. Transitions before since only set the starting
// state.
func backendEvents(bs []store.ActivityBucket, since time.Time) []TrafficEvent {
	type cell struct{ requests, errors int }
	per := map[string]map[int64]cell{}
	var first, last time.Time
	for _, b := range bs {
		if b.Backend == "" || b.Backend == "—" {
			continue
		}
		if per[b.Backend] == nil {
			per[b.Backend] = map[int64]cell{}
		}
		c := per[b.Backend][b.Start.UnixMilli()]
		c.requests += b.Requests
		c.errors += b.Errors
		per[b.Backend][b.Start.UnixMilli()] = c
		if first.IsZero() || b.Start.Before(first) {
			first = b.Start
		}
		if b.Start.After(last) {
			last = b.Start
		}
	}
	mins := int(degradationWindow.Minutes())
	var out []TrafficEvent
	emit := func(e TrafficEvent, name string, at time.Time) {
		if at.Before(since) {
			return
		}
		e.TS = at.UnixMilli()
		e.ID = fmt.Sprintf("%s:%s:%d", e.Kind, name, e.TS)
		out = append(out, e)
	}
	for name, cells := range per {
		failing := false
		var recovery *TrafficEvent // under the threshold since recoveredAt, not yet for a full window
		var recoveredAt time.Time
		for at := first; !at.After(last); at = at.Add(bucket5m) {
			var total, failed int
			for i := range backendWindowBuckets {
				c := cells[at.Add(-time.Duration(i)*bucket5m).UnixMilli()]
				total, failed = total+c.requests, failed+c.errors
			}
			bad := backendFailing(total, failed)
			switch {
			case bad && !failing:
				failing = true
				emit(TrafficEvent{
					Kind: "backend_failing", Tone: "degraded", Title: name + " started failing requests",
					Detail: fmt.Sprintf("%d%% of its %d requests over %d minutes failed with a 5xx or 429.", 100*failed/total, total, mins),
					To:     fmt.Sprintf("/traffic?backend=%s&since=%d", url.QueryEscape(name), at.Add(-degradationWindow+bucket5m).UnixMilli()),
				}, name, at)
			case bad:
				recovery = nil
			case failing && recovery == nil:
				recovery, recoveredAt = &TrafficEvent{
					Kind: "backend_recovered", Tone: "allowed", Title: name + " is back under the failure threshold",
					To: fmt.Sprintf("/traffic?backend=%s&since=%d", url.QueryEscape(name), at.UnixMilli()),
				}, at
				switch {
				case total < backendMinRequests:
					recovery.Detail = fmt.Sprintf("Only %d requests over %d minutes, too few to call it failing.", total, mins)
				case failed < backendMinFailed:
					recovery.Detail = fmt.Sprintf("%d of its %d requests over %d minutes failed, fewer than %d.", failed, total, mins, backendMinFailed)
				default:
					recovery.Detail = fmt.Sprintf("%d of its %d requests over %d minutes failed, under %d%%.", failed, total, mins, int(backendFailShare*100))
				}
			}
			if recovery != nil && at.Sub(recoveredAt) >= degradationWindow-bucket5m {
				emit(*recovery, name, recoveredAt)
				failing, recovery = false, nil
			}
		}
	}
	return out
}

type spendPoint struct {
	Start time.Time
	USD   float64
}

type crossing struct {
	Level  float64
	At     time.Time // start of the bucket the spend crossed in
	Before float64   // spend before that bucket
}

// spendCrossings finds the bucket where cumulative spend, starting from
// cum0, first reached each level × cap. Levels already reached at cum0
// aren't crossed here.
func spendCrossings(cum0 float64, pts []spendPoint, cap float64, levels []float64) []crossing {
	if cap <= 0 {
		return nil
	}
	var out []crossing
	for _, l := range levels {
		cum := cum0
		if cum >= l*cap {
			continue
		}
		for _, p := range pts {
			if cum+p.USD >= l*cap {
				out = append(out, crossing{Level: l, At: p.Start, Before: cum})
				break
			}
			cum += p.USD
		}
	}
	return out
}

// budgetCounts is whether a team and key's spend counts toward a budget, as
// the gateway covers keys: the team, or the key or its project by id.
func (g grouper) budgetCounts(b model.Budget, c store.ScopeSpend) bool {
	return slices.Contains(g.budgetScopes(store.SpendCell{Team: c.Team, KeyID: c.KeyID}), b.ScopeType+":"+b.Scope)
}

// budgetEvents reports when each budget's month-to-date spend crossed 80%
// and 100% of its cap, in the months overlapping [since, now]. Crossings are
// found hourly, then pinned to their 5-minute bucket. The cap is today's: an
// audit log of cap edits would be needed to use the cap in force then.
func (s *Server) budgetEvents(ctx context.Context, t string, since, now time.Time) ([]TrafficEvent, error) {
	bs, err := s.Store.Budgets(ctx, t)
	if err != nil || len(bs) == 0 {
		return nil, err
	}
	g, err := s.grouper(ctx, t)
	if err != nil {
		return nil, err
	}
	in := g.budgetCounts
	points := func(b model.Budget, cs []store.ScopeSpend, from, to time.Time) []spendPoint {
		var out []spendPoint
		for _, c := range cs {
			if c.Start.Before(from) || !c.Start.Before(to) || !in(b, c) {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Start.Equal(c.Start) {
				out[n-1].USD += c.USD
			} else {
				out = append(out, spendPoint{c.Start, c.USD})
			}
		}
		return out
	}

	first, _ := monthBounds(since)
	hourly, err := s.Store.ScopeSpendBuckets(ctx, t, first, now.Add(bucket5m), time.Hour)
	if err != nil {
		return nil, err
	}
	var out []TrafficEvent
	for start := first; start.Before(now); start = start.AddDate(0, 1, 0) {
		end := start.AddDate(0, 1, 0)
		for _, b := range bs {
			for _, c := range spendCrossings(0, points(b, hourly, start, end), b.CapUSD, budgetLevels) {
				fine, err := s.Store.ScopeSpendBuckets(ctx, t, c.At, c.At.Add(time.Hour), bucket5m)
				if err != nil {
					return nil, err
				}
				at := c.At
				if pin := spendCrossings(c.Before, points(b, fine, c.At, c.At.Add(time.Hour)), b.CapUSD, []float64{c.Level}); len(pin) == 1 {
					at = pin[0].At
				}
				if at.Before(since) {
					continue
				}
				e := TrafficEvent{ID: fmt.Sprintf("budget:%s:%g:%d", b.ID, c.Level, start.UnixMilli()), TS: at.UnixMilli(), Tone: "degraded", To: "/spend"}
				if c.Level >= 1 {
					e.Kind, e.Title = "budget_cap", fmt.Sprintf("Budget %q reached its cap", b.ScopeName)
				} else {
					e.Kind, e.Title = "budget_80", fmt.Sprintf("Budget %q reached %d%% of its cap", b.ScopeName, int(c.Level*100))
				}
				e.Detail = fmt.Sprintf("%s spend this month passed %s of its %s %s cap. Measured against today's cap.",
					strings.ToUpper(b.ScopeType[:1])+b.ScopeType[1:], dollars(c.Level*b.CapUSD), dollars(b.CapUSD), b.Period)
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// dollars renders whole dollars with thousands separators: "$12,000".
func dollars(v float64) string {
	s := fmt.Sprintf("%d", int64(math.Round(v)))
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return "$" + s
}

func (s *Server) activity(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ctx := r.Context()
	_, d := rangeDuration(r)
	now := time.Now().UTC()
	since := now.Add(-d)
	all, err := s.Store.Changes(ctx, t, 500, "")
	if err != nil {
		return nil, err
	}
	// Enough buckets before since for the earliest change's before window
	// and the first backend window.
	from := since.Truncate(bucket5m).Add(-impactBuckets * bucket5m)
	bs, err := s.Store.ActivityBuckets(ctx, t, from, now.Add(bucket5m))
	if err != nil {
		return nil, err
	}
	out := ActivityView{Since: since.UnixMilli(), Until: now.UnixMilli(), Changes: []ActivityChange{}}
	for _, c := range all {
		if c.TS < out.Since {
			continue
		}
		im := impactAt(bs, time.UnixMilli(c.TS), now)
		c.Effect, c.EffectTone = im.Effect, im.EffectTone
		out.Changes = append(out.Changes, ActivityChange{Change: c, Impact: im})
	}
	budgets, err := s.budgetEvents(ctx, t, since, now)
	if err != nil {
		return nil, err
	}
	out.Events = append(backendEvents(bs, since), budgets...)
	if out.Events == nil {
		out.Events = []TrafficEvent{}
	}
	slices.SortFunc(out.Events, func(a, b TrafficEvent) int { return cmp.Compare(b.TS, a.TS) })
	return out, nil
}
