package gateway

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// Replay (spec §7.5.7) runs recorded requests through the policies twice,
// as they are and with one policy's draft in place, using the evaluator
// Warden runs (Decision.runPolicies), and reports what the draft changes.
//
// A request whose route captured content replays exactly: its masked prompt
// with each placeholder back as a value its detector matches. One without
// content replays on metadata only (team, project, key, model, provider,
// region): rules that read the prompt are left out on both sides, so it
// shows only what the other rules change.
//
// Only policy evaluation runs: budgets, allowlists and backend health are
// what they were, and every backend counts as healthy, so a result is the
// policies' doing alone. The draft is evaluated as enforced, whatever mode it
// would be published in: that's what it would do once promoted.
type Replay struct {
	base, draft         *Snapshot
	baseMeta, draftMeta *Snapshot
	keys                map[string]*store.KeyRecord
	skipped             []string
	res                 ReplayResult
}

// ReplayItem is one recorded request.
type ReplayItem struct {
	ID        string
	TS        int64
	KeyID     string
	KeyName   string
	Team      string
	ProjectID string
	Model     string // as requested
	Region    string // the x-data-region header it carried
	// Messages is the captured, masked prompt; nil means none was captured.
	Messages []fakellm.Message
}

type ReplayResult struct {
	Total    int        `json:"total"`
	Exact    ReplayPart `json:"exact"`
	Metadata ReplayPart `json:"metadata"`
	// SkippedRules read the prompt, so they didn't run on metadata-only requests.
	SkippedRules []string         `json:"skippedRules"`
	Affected     []ReplayAffected `json:"affected"`
}

// ReplayPart is the change on one kind of request. A request can count under
// more than one change (newly redacted and rerouted, say), but once in Changed.
type ReplayPart struct {
	Requests         int `json:"requests"`
	Changed          int `json:"changed"`
	NewlyBlocked     int `json:"newlyBlocked"`
	NoLongerBlocked  int `json:"noLongerBlocked"`
	NewlyRedacted    int `json:"newlyRedacted"`
	NoLongerRedacted int `json:"noLongerRedacted"`
	NewlyRerouted    int `json:"newlyRerouted"`
	NoLongerRerouted int `json:"noLongerRerouted"`
}

// ReplayAffected is a request the draft would change: what happened to it
// under the policies as they are, and what would under the draft.
type ReplayAffected struct {
	ID    string `json:"id"`
	TS    int64  `json:"ts"`
	Team  string `json:"team"`
	Key   string `json:"key"`
	Model string `json:"model"`
	Kind  string `json:"kind"` // exact | metadata
	From  string `json:"from"`
	To    string `json:"to"`
}

// MaxAffected bounds the requests a result lists; the counts cover all.
const MaxAffected = 50

// NewReplay replays draft in place of the policy with its id (or after the
// others, for one not in the snapshot) against s's policies.
func NewReplay(s *Snapshot, draft model.Policy) *Replay {
	draft.Mode = "enforce"
	r := &Replay{base: replaySnapshot(s, s.Policies), keys: map[string]*store.KeyRecord{}}
	ps := slices.Clone(s.Policies)
	if i := slices.IndexFunc(ps, func(p model.Policy) bool { return p.ID == draft.ID }); i >= 0 {
		ps[i] = draft
	} else {
		ps = append(ps, draft)
	}
	r.draft = replaySnapshot(s, ps)
	var skipBase, skipDraft []string
	r.baseMeta, skipBase = metadataOnly(r.base)
	r.draftMeta, skipDraft = metadataOnly(r.draft)
	r.skipped = slices.Compact(slices.Sorted(slices.Values(append(skipBase, skipDraft...))))
	for _, k := range s.KeyBy {
		r.keys[k.ID] = k
	}
	r.res.SkippedRules, r.res.Affected = []string{}, []ReplayAffected{}
	return r
}

// replaySnapshot is s with policies ps and every backend healthy.
func replaySnapshot(s *Snapshot, ps []model.Policy) *Snapshot {
	c := *s
	c.Policies = ps
	c.Backends = slices.Clone(s.Backends)
	for i := range c.Backends {
		c.Backends[i].Health = "healthy"
	}
	return &c
}

// metadataOnly is s without the active rules that read the prompt, and their names.
func metadataOnly(s *Snapshot) (*Snapshot, []string) {
	c := *s
	c.Policies = slices.Clone(s.Policies)
	var skipped []string
	for i, p := range c.Policies {
		if p.Mode != "enforce" && p.Mode != "monitor" {
			continue
		}
		c.Policies[i].Rules = slices.DeleteFunc(slices.Clone(p.Rules), func(r model.PolicyRule) bool {
			reads := slices.ContainsFunc(r.When, func(c model.Cond) bool { return c.Op == "contains entity" })
			if reads {
				skipped = append(skipped, ruleName(p, r))
			}
			return reads
		})
	}
	return &c, skipped
}

func (r *Replay) Add(it ReplayItem) {
	k := r.keys[it.KeyID]
	if k == nil {
		// Deleted since: the receipt says who it was.
		k = &store.KeyRecord{APIKey: model.APIKey{ID: it.KeyID, Name: it.KeyName, Team: it.Team, ProjectID: it.ProjectID}}
	}
	base, draft, part, kind := r.baseMeta, r.draftMeta, &r.res.Metadata, "metadata"
	var msgs []fakellm.Message
	if it.Messages != nil {
		base, draft, part, kind = r.base, r.draft, &r.res.Exact, "exact"
		msgs = slices.Clone(it.Messages)
		for i := range msgs {
			msgs[i].Content = base.Detectors.Unmask(msgs[i].Content)
		}
	}
	r.res.Total++
	part.Requests++
	in := Input{Region: it.Region, Req: fakellm.ChatRequest{Model: it.Model, Messages: msgs}}
	from, to := evaluate(base, k, in), evaluate(draft, k, in)
	if from.String() == to.String() {
		return
	}
	part.Changed++
	switch {
	case to.blocked && !from.blocked:
		part.NewlyBlocked++
	case from.blocked && !to.blocked:
		part.NoLongerBlocked++
	case !from.blocked:
		if slices.ContainsFunc(slices.Collect(maps.Keys(to.redacted)), func(e string) bool { return from.redacted[e] == 0 }) {
			part.NewlyRedacted++
		}
		if slices.ContainsFunc(slices.Collect(maps.Keys(from.redacted)), func(e string) bool { return to.redacted[e] == 0 }) {
			part.NoLongerRedacted++
		}
		if to.rerouteTo != "" && to.rerouteTo != from.rerouteTo {
			part.NewlyRerouted++
		}
		if from.rerouteTo != "" && to.rerouteTo == "" {
			part.NoLongerRerouted++
		}
	}
	if len(r.res.Affected) < MaxAffected {
		r.res.Affected = append(r.res.Affected, ReplayAffected{ID: it.ID, TS: it.TS, Team: it.Team, Key: k.Name, Model: it.Model, Kind: kind, From: from.String(), To: to.String()})
	}
}

func (r *Replay) Result() ReplayResult {
	out := r.res
	out.SkippedRules = append(out.SkippedRules, r.skipped...)
	return out
}

// outcome is what the policies did to a request.
type outcome struct {
	blocked   bool
	by        string         // the rule that blocked it
	redacted  map[string]int // entity → count
	rerouteTo string
}

func (o outcome) String() string {
	if o.blocked {
		return "blocked by " + o.by
	}
	var parts []string
	if len(o.redacted) > 0 {
		var es []string
		for _, e := range sortedKeys(o.redacted) {
			es = append(es, fmt.Sprintf("%d %s", o.redacted[e], e))
		}
		parts = append(parts, "redacted "+strings.Join(es, ", "))
	}
	if o.rerouteTo != "" {
		parts = append(parts, "rerouted to "+o.rerouteTo)
	}
	if len(parts) == 0 {
		return "allowed"
	}
	return strings.Join(parts, " · ")
}

func evaluate(s *Snapshot, k *store.KeyRecord, in Input) outcome {
	d := &Decision{Req: in.Req, Receipt: &model.Receipt{Redactions: []model.Redaction{}, Rules: []model.RuleEval{}}}
	resolved, _ := store.ResolveAlias(s.Aliases, in.Req.Model)
	current, _ := s.primary(resolved)
	_, _, admitted := d.runPolicies(s, k, in, resolved, current)
	o := outcome{redacted: map[string]int{}}
	if !admitted {
		o.blocked = true
		if n := len(d.Receipt.Rules); n > 0 {
			ev := d.Receipt.Rules[n-1]
			o.by = ev.Name
			if ev.Policy != "" && ev.Policy != ev.Name {
				o.by = ev.Policy + "/" + ev.Name
			}
		}
		return o
	}
	for _, x := range d.Receipt.Redactions {
		o.redacted[x.Type] += x.Count
	}
	if d.rerouted {
		o.rerouteTo = d.rerouteTo
	}
	return o
}
