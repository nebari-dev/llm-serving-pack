package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// Rule writes (§7.5.7): a new rule starts as an unpublished draft; edits go
// to a draft and reach Warden only when published as the next version.
// Published versions are immutable, and rollback republishes an old one.

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

func (s *Server) ruleContent(r *http.Request, t string) (store.RuleContent, error) {
	var c store.RuleContent
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
	if err := store.ValidateRule(c, env); err != nil {
		return c, badRequest(err.Error())
	}
	return c, nil
}

// publishErr turns a publish that can't go ahead into a 409 with its reason.
func publishErr(err error) error {
	var pe store.ErrPublish
	if errors.As(err, &pe) {
		return conflict(pe.Error())
	}
	return err
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	c, err := s.ruleContent(r, t)
	if err != nil {
		return nil, err
	}
	v, err := s.Store.CreateRule(r.Context(), t, actor(r), c)
	if err != nil {
		return nil, err
	}
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

func (s *Server) saveRuleDraft(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	c, err := s.ruleContent(r, t)
	if err != nil {
		return nil, err
	}
	v, err := s.Store.SaveDraft(r.Context(), t, actor(r), r.PathValue("id"), m, c)
	if err != nil {
		return nil, err
	}
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

func (s *Server) discardRuleDraft(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	v, err := s.Store.DiscardDraft(r.Context(), t, actor(r), r.PathValue("id"), m)
	if err != nil {
		return nil, publishErr(err)
	}
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

// RulePublishDryRun is what a publish would change (§6 dryRun). The replay
// against recorded traffic (§7.5.7) isn't connected yet, and says so.
type RulePublishDryRun struct {
	DryRun  bool             `json:"dryRun"`
	Rule    model.PolicyRule `json:"rule"`
	Changes []RuleChange     `json:"changes"`
	Replay  *struct{}        `json:"replay"`
	Note    string           `json:"note"`
}

type RuleChange struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

func ruleChanges(cur, next model.PolicyRule) []RuleChange {
	out := []RuleChange{}
	for _, f := range []struct {
		name     string
		from, to any
	}{
		{"name", cur.Name, next.Name}, {"description", cur.Description, next.Description}, {"mode", cur.Mode, next.Mode},
		{"failMode", cur.FailMode, next.FailMode}, {"when", cur.When, next.When}, {"then", cur.Then, next.Then}, {"version", cur.Version, next.Version},
	} {
		if !reflect.DeepEqual(f.from, f.to) {
			out = append(out, RuleChange{f.name, f.from, f.to})
		}
	}
	return out
}

// publishRule takes {mode?, failMode?}: it publishes the draft, or just a new
// mode or fail mode, as the next version. ?dryRun=true shows the change.
func (s *Server) publishRule(w http.ResponseWriter, r *http.Request, t string) (any, error) {
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
		cur, next, err := s.Store.PlanPublish(r.Context(), t, id, r.Header.Get("If-Match"), in.Mode, in.FailMode)
		if err != nil {
			return nil, publishErr(err)
		}
		return RulePublishDryRun{DryRun: true, Rule: next, Changes: ruleChanges(cur, next),
			Note: "Replay against recorded traffic isn't connected yet, so this shows the rule change only."}, nil
	}
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	v, err := s.Store.Publish(r.Context(), t, actor(r), id, m, in.Mode, in.FailMode)
	if err != nil {
		return nil, publishErr(err)
	}
	s.configChanged()
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

// rollbackRule takes {version}: that version's content and mode go live as
// the next version.
func (s *Server) rollbackRule(w http.ResponseWriter, r *http.Request, t string) (any, error) {
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
	v, err := s.Store.Rollback(r.Context(), t, actor(r), r.PathValue("id"), m, in.Version)
	if err != nil {
		return nil, publishErr(err)
	}
	s.configChanged()
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

func (s *Server) ruleVersions(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	return s.Store.RuleVersions(r.Context(), t, r.PathValue("id"))
}

func (s *Server) deleteRule(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	if err := s.Store.DeleteRule(r.Context(), t, actor(r), id, m); err != nil {
		return nil, publishErr(err)
	}
	s.configChanged()
	return map[string]string{"id": id}, nil
}

// reorderRules takes {"from": [ids], "to": [ids]}: the order the caller saw
// and the one they want. Order decides outcomes, so a stale "from" is a 409.
func (s *Server) reorderRules(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in struct{ From, To []string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	err := s.Store.ReorderRules(r.Context(), t, actor(r), in.From, in.To)
	var bad store.ErrBadOrder
	switch {
	case errors.As(err, &bad):
		return nil, badRequest(bad.Error())
	case errors.Is(err, store.ErrSameOrder):
		return nil, badRequest(err.Error())
	case errors.Is(err, store.ErrConflict):
		return nil, conflict("the rules were reordered or changed since you loaded them")
	case err != nil:
		return nil, err
	}
	s.configChanged()
	return s.rules(nil, r, t)
}
