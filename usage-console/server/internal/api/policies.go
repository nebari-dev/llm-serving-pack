package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"reflect"
	"slices"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// Policy writes (§5.2, §7.5.7): a policy is an ordered list of rules with
// one mode and one fail mode. A new policy starts as an unpublished draft;
// edits go to a draft and reach Warden only when published as the next
// version. Published versions are immutable, and rollback republishes an
// old one.

func (s *Server) ruleEnv(ctx context.Context, t string) (store.RuleEnv, error) {
	var env store.RuleEnv
	reg, _, err := s.detectorRegistry(ctx, t)
	if err != nil {
		return env, err
	}
	env.Entities = reg.Entities() // built-ins and custom entities
	models, err := s.Store.Models(ctx)
	if err != nil {
		return env, err
	}
	for _, m := range models {
		env.Models = append(env.Models, m.ID)
	}
	backends, err := s.Store.Backends(ctx, t)
	if err != nil {
		return env, err
	}
	for _, b := range backends {
		env.Regions = append(env.Regions, b.Region)
	}
	projects, err := s.Store.Projects(ctx, t)
	if err != nil {
		return env, err
	}
	env.Projects = []string{}
	for _, p := range projects {
		if !p.Deleted {
			env.Projects = append(env.Projects, p.ID)
		}
	}
	return env, nil
}

// RuleVocabulary is what a rule may name, so the builder offers exactly what
// ValidateRule accepts: the engine's entities for "contains entity", the
// fields "is" compares, and the models and backend regions "route to" takes.
type RuleVocabulary struct {
	Entities []string `json:"entities"`
	Fields   []string `json:"fields"`
	Targets  []string `json:"targets"`
}

func ruleVocabulary(env store.RuleEnv) RuleVocabulary {
	targets := slices.Concat(env.Models, env.Regions)
	slices.Sort(targets)
	return RuleVocabulary{Entities: env.Entities, Fields: store.RuleFields, Targets: slices.Compact(targets)}
}

func (s *Server) ruleVocabulary(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	env, err := s.ruleEnv(r.Context(), t)
	if err != nil {
		return nil, err
	}
	return ruleVocabulary(env), nil
}

// policies is every policy in order: live version, draft, ETag, reroute
// warnings, and how often its rules matched (24h, and the 7-day daily mean).
func (s *Server) policies(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ps, err := s.Store.Policies(r.Context(), t)
	if err != nil {
		return nil, err
	}
	counts, err := s.Store.PolicyCounts(r.Context(), t)
	if err != nil {
		return nil, err
	}
	drafts, err := s.Store.PolicyDrafts(r.Context(), t)
	if err != nil {
		return nil, err
	}
	out := make([]store.PolicyView, len(ps))
	for i := range ps {
		c := counts[ps[i].ID]
		ps[i].Fired24h, ps[i].Baseline7d = c.Last24h, int(math.Round(float64(c.Last7d)/7))
	}
	for i := range ps {
		out[i] = store.NewPolicyView(ps[i], drafts[ps[i].ID], ps)
	}
	return out, nil
}

func (s *Server) policyContent(r *http.Request, t string) (store.PolicyContent, error) {
	var c store.PolicyContent
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		return c, badRequest("invalid JSON body")
	}
	if c.FailMode == "" {
		c.FailMode = "closed"
	}
	env, err := s.ruleEnv(r.Context(), t)
	if err != nil {
		return c, err
	}
	if err := store.ValidatePolicy(c, env); err != nil {
		return c, badRequest(err.Error())
	}
	return c, nil
}

// publishErr turns a write that can't go ahead into a 409 with its reason.
func publishErr(err error) error {
	var pe store.ErrPublish
	if errors.As(err, &pe) {
		return conflict(pe.Error())
	}
	return err
}

func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	c, err := s.policyContent(r, t)
	if err != nil {
		return nil, err
	}
	v, err := s.Store.CreatePolicy(r.Context(), t, actor(r), c)
	if err != nil {
		return nil, err
	}
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

func (s *Server) savePolicyDraft(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	c, err := s.policyContent(r, t)
	if err != nil {
		return nil, err
	}
	v, err := s.Store.SavePolicyDraft(r.Context(), t, actor(r), r.PathValue("id"), m, c)
	if err != nil {
		return nil, err
	}
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

func (s *Server) discardPolicyDraft(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	v, err := s.Store.DiscardPolicyDraft(r.Context(), t, actor(r), r.PathValue("id"), m)
	if err != nil {
		return nil, publishErr(err)
	}
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

// PolicyPublishDryRun is what a publish would change (§6 dryRun), with the
// reroute conflicts it would have against the other enforcing policies
// (§5.3), and the replay (§7.5.7) of the version it would publish over the
// last hour (?window= for another).
type PolicyPublishDryRun struct {
	DryRun   bool            `json:"dryRun"`
	Policy   model.Policy    `json:"policy"`
	Changes  []PolicyChange  `json:"changes"`
	Warnings []string        `json:"warnings"`
	Replay   *ReplayResponse `json:"replay"`
}

type PolicyChange struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

func policyChanges(cur, next model.Policy) []PolicyChange {
	out := []PolicyChange{}
	for _, f := range []struct {
		name     string
		from, to any
	}{
		{"name", cur.Name, next.Name}, {"description", cur.Description, next.Description}, {"mode", cur.Mode, next.Mode},
		{"failMode", cur.FailMode, next.FailMode}, {"rules", cur.Rules, next.Rules}, {"version", cur.Version, next.Version},
	} {
		if !reflect.DeepEqual(f.from, f.to) {
			out = append(out, PolicyChange{f.name, f.from, f.to})
		}
	}
	return out
}

// publishPolicy takes {mode?, failMode?}: it publishes the draft, or just a
// new mode or fail mode, as the next version. ?dryRun=true shows the change.
func (s *Server) publishPolicy(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in struct {
		Mode     string `json:"mode"`
		FailMode string `json:"failMode"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, badRequest("invalid JSON body")
		}
	}
	switch {
	case in.Mode != "" && in.Mode != "enforce" && in.Mode != "monitor" && in.Mode != "disabled":
		return nil, badRequest("mode must be enforce, monitor or disabled")
	case in.FailMode != "" && in.FailMode != "open" && in.FailMode != "closed":
		return nil, badRequest("failMode must be open or closed")
	}
	id := r.PathValue("id")
	if dryRun(r) {
		cur, next, warnings, err := s.Store.PlanPublish(r.Context(), t, id, r.Header.Get("If-Match"), in.Mode, in.FailMode)
		if err != nil {
			return nil, publishErr(err)
		}
		window := cmp.Or(r.URL.Query().Get("window"), "1h")
		if _, ok := replayWindows[window]; !ok {
			return nil, badRequest("window must be 1h, 24h, 7d or 30d")
		}
		rp, err := s.replay(r.Context(), t, next, window)
		if err != nil {
			return nil, err
		}
		rp.Replayed = "draft"
		return PolicyPublishDryRun{DryRun: true, Policy: next, Changes: policyChanges(cur, next), Warnings: warnings, Replay: rp}, nil
	}
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	v, err := s.Store.PublishPolicy(r.Context(), t, actor(r), id, m, in.Mode, in.FailMode)
	if err != nil {
		return nil, publishErr(err)
	}
	s.configChanged()
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

// rollbackPolicy takes {version}: that version's rules, fail mode and mode
// go live as the next version.
func (s *Server) rollbackPolicy(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	var in struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Version < 1 {
		return nil, badRequest("version is required")
	}
	v, err := s.Store.RollbackPolicy(r.Context(), t, actor(r), r.PathValue("id"), m, in.Version)
	if err != nil {
		return nil, publishErr(err)
	}
	s.configChanged()
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

func (s *Server) policyVersions(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.Store.PolicyVersions(r.Context(), t, r.PathValue("id"))
}

func (s *Server) deletePolicy(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	if err := s.Store.DeletePolicy(r.Context(), t, actor(r), id, m); err != nil {
		return nil, publishErr(err)
	}
	s.configChanged()
	return map[string]string{"id": id}, nil
}

// reorderPolicies takes {"from": [ids], "to": [ids]}: the order the caller
// saw and the one they want. Order decides outcomes, so a stale "from" is a
// 409.
func (s *Server) reorderPolicies(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in struct{ From, To []string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	err := s.Store.ReorderPolicies(r.Context(), t, actor(r), in.From, in.To)
	var bad store.ErrBadOrder
	switch {
	case errors.As(err, &bad):
		return nil, badRequest(bad.Error())
	case errors.Is(err, store.ErrSameOrder):
		return nil, badRequest(err.Error())
	case errors.Is(err, store.ErrConflict):
		return nil, conflict("the policies were reordered or changed since you loaded them")
	case err != nil:
		return nil, err
	}
	s.configChanged()
	return s.policies(nil, r, t)
}
