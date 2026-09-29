package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// Rule writes (§7.5.7): a new rule starts as an unpublished draft; edits go
// to a draft and reach Warden only when published as the next version.
// Published versions are immutable, and rollback republishes an old one.

func (s *Server) ruleEnv(ctx context.Context, t string) (store.RuleEnv, error) {
	env := store.RuleEnv{Entities: gateway.Entities()}
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
	return env, nil
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
	v, err := s.Store.CreateRule(r.Context(), t, s.DevActor, c)
	if err != nil {
		return nil, err
	}
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

func (s *Server) saveRuleDraft(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	c, err := s.ruleContent(r, t)
	if err != nil {
		return nil, err
	}
	v, err := s.Store.SaveDraft(r.Context(), t, s.DevActor, r.PathValue("id"), r.Header.Get("If-Match"), c)
	if err != nil {
		return nil, err
	}
	w.Header().Set("ETag", v.ETag)
	return v, nil
}

func (s *Server) discardRuleDraft(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	v, err := s.Store.DiscardDraft(r.Context(), t, s.DevActor, r.PathValue("id"), r.Header.Get("If-Match"))
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
	id, ifMatch := r.PathValue("id"), r.Header.Get("If-Match")
	if dryRun(r) {
		cur, next, err := s.Store.PlanPublish(r.Context(), t, id, ifMatch, in.Mode, in.FailMode)
		if err != nil {
			return nil, publishErr(err)
		}
		return RulePublishDryRun{DryRun: true, Rule: next, Changes: ruleChanges(cur, next),
			Note: "Replay against recorded traffic isn't connected yet, so this shows the rule change only."}, nil
	}
	v, err := s.Store.Publish(r.Context(), t, s.DevActor, id, ifMatch, in.Mode, in.FailMode)
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
	var in struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Version < 1 {
		return nil, badRequest("version is required")
	}
	v, err := s.Store.Rollback(r.Context(), t, s.DevActor, r.PathValue("id"), r.Header.Get("If-Match"), in.Version)
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
	id := r.PathValue("id")
	if err := s.Store.DeleteRule(r.Context(), t, s.DevActor, id, r.Header.Get("If-Match")); err != nil {
		return nil, publishErr(err)
	}
	s.configChanged()
	return map[string]string{"id": id}, nil
}

