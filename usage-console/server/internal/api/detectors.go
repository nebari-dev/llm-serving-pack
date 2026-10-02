package api

import (
	"net/http"
	"slices"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// DetectorView is one of the engine's entity detectors (§7.5.7): how it
// matches, the live rules that use it, and its last 24 hours from receipts.
// These are regexes with no confidence score, so there's no threshold.
type DetectorView struct {
	gateway.DetectorInfo
	UsedBy              []DetectorUse `json:"usedBy"`
	RedactedRequests24h int           `json:"redactedRequests24h"`
	RedactedMatches24h  int           `json:"redactedMatches24h"`
	Blocked24h          int           `json:"blocked24h"`
}

type DetectorUse struct {
	Rule    string `json:"rule"`
	Version int    `json:"version"`
	Mode    string `json:"mode"`
	Action  string `json:"action"`
}

// detectorViews joins each detector to the live rules whose prompt
// conditions name it, in rule order, and to its receipt counts.
func detectorViews(ds []gateway.DetectorInfo, rules []model.PolicyRule, hits map[string]store.DetectorHits) []DetectorView {
	out := make([]DetectorView, 0, len(ds))
	for _, d := range ds {
		v := DetectorView{DetectorInfo: d, UsedBy: []DetectorUse{}}
		for _, r := range rules {
			if r.Mode != "enforce" && r.Mode != "monitor" {
				continue
			}
			uses := slices.ContainsFunc(r.When, func(c model.Cond) bool {
				return c.Field == "prompt" && c.Op == "contains entity" && slices.Contains(c.Value, d.Entity)
			})
			if uses {
				action := ""
				if len(r.Then) > 0 {
					action = r.Then[0].Action
				}
				v.UsedBy = append(v.UsedBy, DetectorUse{r.Name, r.Version, r.Mode, action})
			}
		}
		h := hits[d.Entity]
		v.RedactedRequests24h, v.RedactedMatches24h, v.Blocked24h = h.RedactedRequests, h.RedactedMatches, h.Blocked
		out = append(out, v)
	}
	return out
}

func (s *Server) detectors(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	rules, err := s.Store.Rules(r.Context(), t)
	if err != nil {
		return nil, err
	}
	hits, err := s.Store.DetectorHits(r.Context(), t)
	if err != nil {
		return nil, err
	}
	return detectorViews(gateway.DetectorList(), rules, hits), nil
}
