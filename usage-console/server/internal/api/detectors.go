package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// DetectorView is one of the engine's entity detectors (§7.5.7): how it
// matches, the live rules that use it, and its last 24 hours from receipts.
// These are regexes with no confidence score, so there's no threshold.
// False positives and confirmations are reviewers' verdicts on hits whose
// receipts are from the last 30 days. A custom entity carries its registry
// row, for editing with If-Match.
type DetectorView struct {
	gateway.DetectorInfo
	UsedBy              []DetectorUse       `json:"usedBy"`
	RedactedRequests24h int                 `json:"redactedRequests24h"`
	RedactedMatches24h  int                 `json:"redactedMatches24h"`
	Blocked24h          int                 `json:"blocked24h"`
	FalsePositives30d   int                 `json:"falsePositives30d"`
	Confirmed30d        int                 `json:"confirmed30d"`
	CustomEntity        *store.CustomEntity `json:"customEntity,omitempty"`
}

// DetectorUse is a live rule that names a detector: the rule, its policy
// (version and mode are the policy's), and the rule's actions.
type DetectorUse struct {
	Rule    string `json:"rule"`
	Policy  string `json:"policy"`
	Version int    `json:"version"`
	Mode    string `json:"mode"`
	Action  string `json:"action"`
}

// detectorViews joins each detector to the rules of live policies whose
// prompt conditions name it, in evaluation order, to its receipt counts and
// reviewers' verdicts, and a custom one to its registry row.
func detectorViews(ds []gateway.DetectorInfo, custom []store.CustomEntity, policies []model.Policy, hits map[string]store.DetectorHits, verdicts map[string]store.VerdictCounts) []DetectorView {
	out := make([]DetectorView, 0, len(ds))
	for _, d := range ds {
		v := DetectorView{DetectorInfo: d, UsedBy: []DetectorUse{}}
		for _, p := range policies {
			if p.Mode != "enforce" && p.Mode != "monitor" {
				continue
			}
			for _, r := range p.Rules {
				if names(r.When, d.Entity) {
					acts := make([]string, len(r.Then))
					for i, a := range r.Then {
						acts[i] = a.Action
					}
					v.UsedBy = append(v.UsedBy, DetectorUse{r.Name, p.Name, p.Version, p.Mode, strings.Join(acts, " + ")})
				}
			}
		}
		h := hits[d.Entity]
		v.RedactedRequests24h, v.RedactedMatches24h, v.Blocked24h = h.RedactedRequests, h.RedactedMatches, h.Blocked
		c := verdicts[d.Entity]
		v.FalsePositives30d, v.Confirmed30d = c.FalsePositives, c.Confirmed
		if d.Custom {
			if i := slices.IndexFunc(custom, func(e store.CustomEntity) bool { return e.Name == d.Entity }); i >= 0 {
				v.CustomEntity = &custom[i]
			}
		}
		out = append(out, v)
	}
	return out
}

// names reports whether conditions name entity in "contains entity".
func names(when []model.Cond, entity string) bool {
	return slices.ContainsFunc(when, func(c model.Cond) bool {
		return c.Field == "prompt" && c.Op == "contains entity" && slices.Contains(c.Value, entity)
	})
}

// detectorRegistry is the detectors the engine runs for t: the built-ins
// and t's custom entities, as Warden loads them.
func (s *Server) detectorRegistry(ctx context.Context, t string) (*gateway.Detectors, []store.CustomEntity, error) {
	custom, err := s.Store.CustomEntities(ctx, t)
	if err != nil {
		return nil, nil, err
	}
	reg, err := gateway.NewDetectors(custom)
	if err != nil {
		// Validated on write, so this is a pattern that stopped passing; the
		// registry leaves it out, as the snapshot does.
		log.Printf("detectors %s: %v", t, err)
	}
	return reg, custom, nil
}

func (s *Server) detectors(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	reg, custom, err := s.detectorRegistry(r.Context(), t)
	if err != nil {
		return nil, err
	}
	policies, err := s.Store.Policies(r.Context(), t)
	if err != nil {
		return nil, err
	}
	hits, err := s.Store.DetectorHits(r.Context(), t)
	if err != nil {
		return nil, err
	}
	verdicts, err := s.Store.VerdictCounts(r.Context(), t)
	if err != nil {
		return nil, err
	}
	return detectorViews(reg.List(), custom, policies, hits, verdicts), nil
}

// ---- custom entities (§5.3 registry) ---------------------------------------

// EntityInput is a custom entity as an author writes it. Sample is text to
// try the pattern on in a dry run; it's never stored.
type EntityInput struct {
	Name         string   `json:"name"`
	Pattern      string   `json:"pattern"`
	Label        string   `json:"label"`
	MustMatch    []string `json:"mustMatch"`
	MustNotMatch []string `json:"mustNotMatch"`
	Sample       string   `json:"sample"`
}

// EntityCheck is a dry run's answer: the entity would save, and what its
// pattern finds in the sample, with the sample as a redact would leave it.
type EntityCheck struct {
	DryRun   bool               `json:"dryRun"`
	Entity   store.CustomEntity `json:"entity"`
	Matches  []string           `json:"matches"`
	Redacted string             `json:"redacted"`
}

func entityInput(r *http.Request) (EntityInput, error) {
	var in EntityInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return in, badRequest("invalid JSON body")
	}
	in.Name, in.Label = strings.TrimSpace(in.Name), strings.TrimSpace(in.Label)
	return in, nil
}

// checkEntity validates e against the registry, and on a dry run answers
// with what its pattern does to the sample.
func checkEntity(e store.CustomEntity, custom []store.CustomEntity, in EntityInput, dry bool) (any, error) {
	if err := gateway.ValidateCustomEntity(e, custom); err != nil {
		return nil, badRequest(err.Error())
	}
	if !dry {
		return nil, nil
	}
	if len(in.Sample) > 10_000 {
		return nil, badRequest("sample is at most 10,000 characters")
	}
	m, red := gateway.SampleMatches(e.Pattern, e.Label, in.Sample)
	return EntityCheck{DryRun: true, Entity: e, Matches: m, Redacted: red}, nil
}

// createEntity takes an EntityInput; ?dryRun=true checks it and tries it on
// the sample without saving.
func (s *Server) createEntity(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	in, err := entityInput(r)
	if err != nil {
		return nil, err
	}
	_, custom, err := s.detectorRegistry(r.Context(), t)
	if err != nil {
		return nil, err
	}
	e := store.CustomEntity{Name: in.Name, Pattern: in.Pattern, Label: in.Label, MustMatch: in.MustMatch, MustNotMatch: in.MustNotMatch}
	if v, err := checkEntity(e, custom, in, dryRun(r)); v != nil || err != nil {
		return v, err
	}
	out, err := s.Store.CreateCustomEntity(r.Context(), t, actor(r), e)
	if errors.Is(err, store.ErrConflict) {
		return nil, conflict(fmt.Sprintf("a custom entity named %q was just added", e.Name))
	}
	if err != nil {
		return nil, err
	}
	s.configChanged()
	w.Header().Set("ETag", out.ETag)
	return out, nil
}

// updateEntity changes an entity's pattern, label and examples, with
// If-Match. The name can't change: rules name the entity by it.
func (s *Server) updateEntity(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	in, err := entityInput(r)
	if err != nil {
		return nil, err
	}
	_, custom, err := s.detectorRegistry(r.Context(), t)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	i := slices.IndexFunc(custom, func(e store.CustomEntity) bool { return e.ID == id })
	if i < 0 {
		return nil, store.ErrNotFound
	}
	if in.Name != "" && in.Name != custom[i].Name {
		return nil, badRequest("an entity's name can't change: rules name it. Add a new entity instead.")
	}
	e := store.CustomEntity{ID: id, Name: custom[i].Name, Pattern: in.Pattern, Label: in.Label, MustMatch: in.MustMatch, MustNotMatch: in.MustNotMatch}
	if v, err := checkEntity(e, custom, in, dryRun(r)); v != nil || err != nil {
		return v, err
	}
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	out, err := s.Store.UpdateCustomEntity(r.Context(), t, actor(r), id, m, e)
	if err != nil {
		return nil, err
	}
	s.configChanged()
	w.Header().Set("ETag", out.ETag)
	return out, nil
}

// entityInUse refuses to delete an entity a policy's rule names, live (in
// any mode), in its draft, or in one of its published versions (newest
// first), which a rollback would bring back: the rule would silently stop
// matching. Each policy is named once, where it's found first.
func entityInUse(entity string, policies []model.Policy, drafts map[string]*store.PolicyDraft, versions map[string][]store.PolicyVersion) error {
	anyNames := func(rs []model.PolicyRule) bool {
		return slices.ContainsFunc(rs, func(r model.PolicyRule) bool { return names(r.When, entity) })
	}
	var by []string
	old := false
	for _, p := range policies {
		if anyNames(p.Rules) {
			by = append(by, p.Name)
			continue
		}
		if d := drafts[p.ID]; d != nil && anyNames(d.Rules) {
			by = append(by, p.Name+" (draft)")
			continue
		}
		for _, v := range versions[p.ID] {
			if anyNames(v.Rules) {
				by, old = append(by, fmt.Sprintf("%s (v%d)", p.Name, v.Version)), true
				break
			}
		}
	}
	tail := ""
	if old {
		tail = " A published version counts: rolling back to it would bring the entity back."
	}
	switch len(by) {
	case 0:
		return nil
	case 1:
		return conflict(fmt.Sprintf("Policy %s names %s. Take it out of that policy first.%s", by[0], entity, tail))
	}
	return conflict(fmt.Sprintf("Policies %s and %s name %s. Take it out of those policies first.%s", strings.Join(by[:len(by)-1], ", "), by[len(by)-1], entity, tail))
}

func (s *Server) deleteEntity(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	_, custom, err := s.detectorRegistry(r.Context(), t)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	i := slices.IndexFunc(custom, func(e store.CustomEntity) bool { return e.ID == id })
	if i < 0 {
		return nil, store.ErrNotFound
	}
	policies, err := s.Store.Policies(r.Context(), t)
	if err != nil {
		return nil, err
	}
	drafts, err := s.Store.PolicyDrafts(r.Context(), t)
	if err != nil {
		return nil, err
	}
	versions, err := s.Store.AllPolicyVersions(r.Context(), t)
	if err != nil {
		return nil, err
	}
	if err := entityInUse(custom[i].Name, policies, drafts, versions); err != nil {
		return nil, err
	}
	if err := s.Store.DeleteCustomEntity(r.Context(), t, actor(r), id, m); err != nil {
		return nil, err
	}
	s.configChanged()
	return map[string]string{"id": id}, nil
}

// ---- false-positive review -------------------------------------------------

// hitWindow is how far back the review queue reaches.
const hitWindow = 7 * 24 * time.Hour

// DetectorHit is one entity a receipt recorded, for review. Receipts keep
// hashes, not prompts, so this is what there is to judge by: the entity,
// how many matches, the action and the rules that took it, who sent the
// request and when, and whether the backend captured content (which, if
// so, holds the prompt as sent upstream: placeholders, not the match).
type DetectorHit struct {
	ReceiptID       string                 `json:"receiptId"`
	TS              int64                  `json:"ts"`
	Entity          string                 `json:"entity"`
	Action          string                 `json:"action"` // redacted | blocked
	Count           int                    `json:"count"`
	Rules           []string               `json:"rules"`
	KeyName         string                 `json:"keyName"`
	Team            string                 `json:"team"`
	Model           string                 `json:"model"`
	ContentCaptured bool                   `json:"contentCaptured"`
	Verdict         *store.DetectorVerdict `json:"verdict"`
	ETag            string                 `json:"etag"`
}

var blockedEntity = regexp.MustCompile(`matched entity "([^"]+)"`)

// hitsOf is a receipt's detector hits: one per redacted type, and the
// entity a policy block names. Monitor-mode matches ("would redact") record
// no entity, so they aren't hits.
func hitsOf(r model.Receipt) []DetectorHit {
	rules := func(action string) []string {
		out := []string{}
		for _, e := range r.Rules {
			// A rule may take several actions ("redact + route to"); its
			// policy names it when the names differ.
			if e.Matched && slices.Contains(strings.Split(e.Action, " + "), action) {
				name := e.Name
				if e.Policy != "" && e.Policy != e.Name {
					name = e.Policy + "/" + e.Name
				}
				out = append(out, fmt.Sprintf("%s v%d", name, e.Version))
			}
		}
		return out
	}
	base := DetectorHit{ReceiptID: r.ID, TS: r.TS, KeyName: r.KeyName, Team: r.Team, Model: r.ResolvedModel, ContentCaptured: r.ContentCaptured}
	var out []DetectorHit
	for _, x := range r.Redactions {
		h := base
		h.Entity, h.Action, h.Count, h.Rules = x.Type, "redacted", x.Count, rules("redact")
		out = append(out, h)
	}
	if r.ErrorCode == "policy_blocked" {
		if m := blockedEntity.FindStringSubmatch(r.ErrorDetail); m != nil {
			h := base
			h.Entity, h.Action, h.Count, h.Rules = m[1], "blocked", 1, rules("block")
			out = append(out, h)
		}
	}
	return out
}

// hitQueue joins hits to their verdicts, keeping entity's only (all when
// empty), and only unreviewed ones if asked.
func hitQueue(hits []DetectorHit, verdicts map[store.HitKey]store.DetectorVerdict, entity string, unreviewed bool) []DetectorHit {
	out := []DetectorHit{}
	for _, h := range hits {
		if entity != "" && h.Entity != entity {
			continue
		}
		if v, ok := verdicts[store.HitKey{ReceiptID: h.ReceiptID, Entity: h.Entity}]; ok {
			if unreviewed {
				continue
			}
			h.Verdict = &v
		}
		h.ETag = store.VerdictETag(h.Verdict)
		out = append(out, h)
	}
	return out
}

// detectorHits is the review queue: GET /detectors/hits?entity=&review=all.
// By default it's the last 7 days' unreviewed hits, newest first.
func (s *Server) detectorHits(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	since := time.Now().Add(-hitWindow)
	rs, err := s.Store.DetectorHitReceipts(r.Context(), t, since, intParam(r, "limit", 200, 1, 1000))
	if err != nil {
		return nil, err
	}
	verdicts, err := s.Store.DetectorVerdicts(r.Context(), t, since.Add(-time.Minute))
	if err != nil {
		return nil, err
	}
	var hits []DetectorHit
	for _, rc := range rs {
		hits = append(hits, hitsOf(rc)...)
	}
	return hitQueue(hits, verdicts, r.URL.Query().Get("entity"), r.URL.Query().Get("review") != "all"), nil
}

// setVerdict takes {receiptId, entity, verdict} with If-Match (the hit's
// etag from the queue): the reviewer's call, audited. The hit must be one
// the receipt records.
func (s *Server) setVerdict(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in struct {
		ReceiptID string `json:"receiptId"`
		Entity    string `json:"entity"`
		Verdict   string `json:"verdict"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	if in.Verdict != "false_positive" && in.Verdict != "confirmed" {
		return nil, badRequest("verdict must be false_positive or confirmed")
	}
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	rc, err := s.Store.Receipt(r.Context(), t, in.ReceiptID, 0)
	if errors.Is(err, store.ErrNotFound) {
		return nil, badRequest("no receipt " + in.ReceiptID + " in the last 30 days")
	}
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(hitsOf(rc), func(h DetectorHit) bool { return h.Entity == in.Entity })
	if i < 0 {
		return nil, badRequest(fmt.Sprintf("receipt %s records no %s hit", in.ReceiptID, in.Entity))
	}
	v, err := s.Store.SetDetectorVerdict(r.Context(), t, actor(r), store.DetectorVerdict{ReceiptID: rc.ID, ReceiptTS: rc.TS, Entity: in.Entity, Verdict: in.Verdict}, m)
	if err != nil {
		return nil, err
	}
	h := hitsOf(rc)[i]
	h.Verdict, h.ETag = &v, store.VerdictETag(&v)
	return h, nil
}
