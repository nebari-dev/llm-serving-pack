package api

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

func (s *Server) aliases(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	aliases, err := s.Store.Aliases(r.Context(), t)
	if err != nil {
		return nil, err
	}
	requested, err := s.Store.RequestedModels(r.Context(), t, time.Now().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	return aliasViews(aliases, requested), nil
}

// putAlias takes {"target": model}: it creates the alias in the path
// (If-None-Match: *), or points it at a new target (If-Match: its etag).
func (s *Server) putAlias(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	// An update names the version it replaces; a create says there's none.
	match, create := r.Header.Get("If-Match"), r.Header.Get("If-None-Match") == "*"
	if match == "" && !create {
		return nil, preconditionRequired("send If-Match with the alias's etag to change it, or If-None-Match: * to create it")
	}
	var in struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	alias := r.PathValue("alias")
	models, err := s.Store.Models(r.Context())
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	if err := store.ValidateAlias(alias, in.Target, ids); err != nil {
		return nil, badRequest(err.Error())
	}
	if err := s.Store.PutAlias(r.Context(), t, s.DevActor, alias, in.Target, match, create); err != nil {
		return nil, err
	}
	s.configChanged()
	return s.aliasView(w, r, t, alias)
}

func (s *Server) deleteAlias(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	alias := r.PathValue("alias")
	if err := s.Store.DeleteAlias(r.Context(), t, s.DevActor, alias, m); err != nil {
		return nil, err
	}
	s.configChanged()
	return map[string]string{"alias": alias}, nil
}

// aliasView is one alias as GET /aliases shows it, with its ETag set.
func (s *Server) aliasView(w http.ResponseWriter, r *http.Request, t, alias string) (any, error) {
	all, err := s.aliases(w, r, t)
	if err != nil {
		return nil, err
	}
	for _, a := range all.([]model.Alias) {
		if a.Alias == alias {
			w.Header().Set("ETag", a.ETag)
			return a, nil
		}
	}
	return nil, store.ErrNotFound
}

// aliasViews credits each requested model's count to the alias the gateway
// would resolve it through, so requests later rerouted still count.
func aliasViews(aliases map[string]string, requested map[string]int) []model.Alias {
	byAlias := map[string]int{}
	for m, n := range requested {
		if a, ok := store.MatchAlias(aliases, m); ok {
			byAlias[a] += n
		}
	}
	out := make([]model.Alias, 0, len(aliases))
	for a, target := range aliases {
		out = append(out, model.Alias{Alias: a, Target: target, Requests24h: byAlias[a], ETag: store.ETag(store.AliasRow{Alias: a, Target: target})})
	}
	slices.SortFunc(out, func(x, y model.Alias) int { return cmp.Compare(x.Alias, y.Alias) })
	return out
}
