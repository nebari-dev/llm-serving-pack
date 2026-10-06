package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/model"
)

// Policies (spec §5.2): a policy is a named, ordered list of rules with one
// mode and one fail mode. It's versioned as a unit: an edit waits in
// policy_drafts until it's published as the next version, published
// versions are immutable (policy_versions), and rollback republishes an old
// one. policy_rules holds the live version's rules, which Warden enforces.

// PolicyContent is what an author writes. Mode and version come from
// publishing. A rule without an id gets one when it's saved.
type PolicyContent struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	FailMode    string             `json:"failMode"`
	Rules       []model.PolicyRule `json:"rules"`
}

// RuleEnv is what a rule may refer to: the engine's entity detectors, the
// catalog models and backend regions a "route to" can target, and the
// project ids a project condition names (nil: not checked).
type RuleEnv struct {
	Entities, Models, Regions, Projects []string
}

var (
	slugName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	ruleID   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

// RuleFields are what "is" and "is not" compare, as the engine reads them.
var RuleFields = []string{"team", "project", "key", "model", "provider", "header x-data-region"}

// ValidateRule checks one rule against what the engine can evaluate. A rule
// may take several actions, at most one of each kind (§5.3): block refuses
// the request, so it stands alone.
func ValidateRule(r model.PolicyRule, env RuleEnv) error {
	switch {
	case !slugName.MatchString(r.Name):
		return errors.New("rule name must be lowercase letters, digits and dashes")
	case len(r.When) == 0:
		return errors.New("a rule needs at least one condition")
	}
	hasEntity := false
	for i, c := range r.When {
		n := i + 1
		switch c.Op {
		case "contains entity":
			if c.Field != "prompt" {
				return fmt.Errorf("condition %d: contains entity applies to the prompt", n)
			}
			for _, e := range c.Value {
				if !slices.Contains(env.Entities, e) {
					return fmt.Errorf("condition %d: unknown entity %q", n, e)
				}
			}
			hasEntity = true
		case "is", "is not", "equals", "not equals":
			if !slices.Contains(RuleFields, c.Field) {
				return fmt.Errorf("condition %d: unknown field %q", n, c.Field)
			}
			if c.Field == "project" && env.Projects != nil {
				for _, v := range c.Value {
					if !slices.Contains(env.Projects, v) {
						return fmt.Errorf("condition %d: unknown project %q (name projects by id)", n, v)
					}
				}
			}
		default:
			return fmt.Errorf("condition %d: unknown op %q", n, c.Op)
		}
		if len(c.Value) == 0 {
			return fmt.Errorf("condition %d: needs at least one value", n)
		}
	}
	if len(r.Then) == 0 {
		return errors.New("a rule needs at least one action")
	}
	count := map[string]int{}
	for _, a := range r.Then {
		switch a.Action {
		case "block":
		case "redact":
			if !hasEntity {
				return errors.New("redact needs a contains entity condition to know what to redact")
			}
		case "route to":
			if !slices.Contains(env.Models, a.Detail) && !slices.Contains(env.Regions, a.Detail) {
				return fmt.Errorf("route to %q: not a catalog model or backend region", a.Detail)
			}
		default:
			return fmt.Errorf("unknown action %q", a.Action)
		}
		count[a.Action]++
	}
	switch {
	case count["block"] > 1:
		return errors.New("two block actions: one is enough")
	case count["block"] == 1 && len(r.Then) > 1:
		var others []string
		for _, a := range r.Then {
			if a.Action != "block" && !slices.Contains(others, a.Action) {
				others = append(others, a.Action)
			}
		}
		return fmt.Errorf("block wins: a blocked request is refused, so %s would never run. Keep block alone, or move them to another rule", strings.Join(others, " and "))
	case count["redact"] > 1:
		return errors.New("one redact per rule: it removes every entity the conditions find")
	case count["route to"] > 1:
		return errors.New("one route to per rule: only the last would apply")
	}
	return nil
}

// ValidatePolicy checks a policy and each of its rules; a rule's error
// names the rule.
func ValidatePolicy(c PolicyContent, env RuleEnv) error {
	switch {
	case !slugName.MatchString(c.Name):
		return errors.New("name must be lowercase letters, digits and dashes")
	case c.FailMode != "open" && c.FailMode != "closed":
		return errors.New("failMode must be open or closed")
	case len(c.Rules) == 0:
		return errors.New("a policy needs at least one rule")
	}
	names, ids := map[string]bool{}, map[string]bool{}
	for _, r := range c.Rules {
		if names[r.Name] {
			return fmt.Errorf("two rules are named %q", r.Name)
		}
		names[r.Name] = true
		if r.ID != "" {
			if !ruleID.MatchString(r.ID) {
				return fmt.Errorf("rule %q: id %q must be letters, digits, dashes or underscores", r.Name, r.ID)
			}
			if ids[r.ID] {
				return fmt.Errorf("rule %q: id %q is taken by another rule", r.Name, r.ID)
			}
			ids[r.ID] = true
		}
		if err := ValidateRule(r, env); err != nil {
			return fmt.Errorf("rule %q: %w", r.Name, err)
		}
	}
	return nil
}

// withRuleIDs is rules with an id for each that lacks one.
func withRuleIDs(rules []model.PolicyRule, newID func() string) []model.PolicyRule {
	out := slices.Clone(rules)
	for i := range out {
		if out[i].ID == "" {
			out[i].ID = newID()
		}
	}
	return out
}

func randomID(prefix string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func reroutes(r model.PolicyRule) (string, bool) {
	for i := len(r.Then) - 1; i >= 0; i-- {
		if r.Then[i].Action == "route to" {
			return r.Then[i].Detail, true
		}
	}
	return "", false
}

// PolicyWarnings are §5.3's authoring-time reroute conflicts for c, the
// content of policy id ("" for a new one, which goes last): rules in it
// that both reroute, and enforcing policies before or after it that reroute
// too. The last reroute wins.
func PolicyWarnings(id string, c PolicyContent, all []model.Policy) []string {
	var out []string
	var routing []model.PolicyRule
	for _, r := range c.Rules {
		if _, ok := reroutes(r); ok {
			routing = append(routing, r)
		}
	}
	if len(routing) == 0 {
		return nil
	}
	for i := 1; i < len(routing); i++ {
		to, _ := reroutes(routing[i])
		out = append(out, fmt.Sprintf("Rules %s and %s both reroute. When both match, %s's route to %s wins: the last reroute wins.",
			routing[i-1].Name, routing[i].Name, routing[i].Name, to))
	}
	at := len(all)
	for i, p := range all {
		if p.ID == id {
			at = i
		}
	}
	for i, p := range all {
		if i == at || p.Mode != "enforce" {
			continue
		}
		var names []string
		for _, r := range p.Rules {
			if _, ok := reroutes(r); ok {
				names = append(names, r.Name)
			}
		}
		if len(names) == 0 {
			continue
		}
		which := "rule " + names[0]
		if len(names) > 1 {
			which = "rules " + strings.Join(names, ", ")
		}
		if i < at {
			out = append(out, fmt.Sprintf("Policy %s runs before this one and reroutes too (%s). When both match, this policy's route wins.", p.Name, which))
		} else {
			out = append(out, fmt.Sprintf("Policy %s runs after this one and reroutes too (%s). When both match, its route wins.", p.Name, which))
		}
	}
	return out
}

// planPublish is the policy a publish would leave live. With a draft, its
// content becomes the next version; mode and failMode, when set, change too.
// A policy's first publish defaults to monitor mode (§7.5.7); later ones
// keep the live mode.
func planPublish(cur model.Policy, draft *PolicyContent, mode, failMode string) (model.Policy, string, error) {
	if mode != "" && mode != "enforce" && mode != "monitor" && mode != "disabled" {
		return cur, "", errors.New("mode must be enforce, monitor or disabled")
	}
	if failMode != "" && failMode != "open" && failMode != "closed" {
		return cur, "", errors.New("failMode must be open or closed")
	}
	if draft == nil && cur.Version == 0 {
		return cur, "", errors.New("no draft to publish")
	}
	next := cur
	if draft != nil {
		next.Name, next.Description, next.FailMode, next.Rules = draft.Name, draft.Description, draft.FailMode, draft.Rules
	}
	switch {
	case mode != "":
		next.Mode = mode
	case cur.Version == 0:
		next.Mode = "monitor"
	}
	if failMode != "" {
		next.FailMode = failMode
	}
	if draft == nil && next.Mode == cur.Mode && next.FailMode == cur.FailMode {
		return cur, "", errors.New("nothing to publish: no draft, and mode and fail mode are unchanged")
	}
	next.Version = cur.Version + 1
	return next, publishAction(next.Mode), nil
}

func publishAction(mode string) string {
	switch mode {
	case "monitor":
		return "Published policy in monitor mode"
	case "disabled":
		return "Disabled policy"
	}
	return "Published policy"
}

// ErrPublish is a write that can't go ahead as asked (planPublish's reasons,
// and the like).
type ErrPublish struct{ error }

// PolicyDraft is a pending edit to a policy, not yet live.
type PolicyDraft struct {
	PolicyContent
	UpdatedAt int64  `json:"updatedAt"` // epoch ms
	UpdatedBy string `json:"updatedBy"`
}

// PolicyView is a policy as the API shows it: the live version, any pending
// draft, the ETag that covers both, and the reroute conflicts of what's
// being authored (the draft, else the live version).
type PolicyView struct {
	model.Policy
	Draft    *PolicyDraft `json:"draft"`
	ETag     string       `json:"etag"`
	Warnings []string     `json:"warnings"`
}

func policyETag(p model.Policy, d *PolicyDraft) string {
	var dc *PolicyContent
	if d != nil {
		dc = &d.PolicyContent
	}
	return ETag([]any{p.ID, p.Name, p.Description, p.Mode, p.FailMode, p.Version, p.Rules, dc})
}

// NewPolicyView pairs a policy with its draft and ETag. all is every policy
// in order, for the warnings; nil leaves them out.
func NewPolicyView(p model.Policy, d *PolicyDraft, all []model.Policy) PolicyView {
	c := PolicyContent{Name: p.Name, Description: p.Description, FailMode: p.FailMode, Rules: p.Rules}
	if d != nil {
		c = d.PolicyContent
	}
	w := PolicyWarnings(p.ID, c, all)
	if w == nil {
		w = []string{}
	}
	return PolicyView{Policy: p, Draft: d, ETag: policyETag(p, d), Warnings: w}
}

// PolicyVersion is one published version of a policy.
type PolicyVersion struct {
	Version int `json:"version"`
	PolicyContent
	Mode        string  `json:"mode"`
	PublishedAt *int64  `json:"publishedAt"` // null: from before versions were kept
	PublishedBy *string `json:"publishedBy"`
}

const policyCols = `id, ordinal, name, description, mode, fail_mode, version`

func scanPolicy(r pgx.Row) (model.Policy, error) {
	p := model.Policy{Rules: []model.PolicyRule{}}
	err := r.Scan(&p.ID, &p.Ordinal, &p.Name, &p.Description, &p.Mode, &p.FailMode, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// liveRules is the live rules of the tenant's policies (or of one, by id),
// by policy, in order.
func liveRules(ctx context.Context, q queryer, tenant, id string) (map[string][]model.PolicyRule, error) {
	rows, err := q.Query(ctx, `
		SELECT r.policy_id, r.id, r.name, r."when", r."then"
		FROM policy_rules r JOIN policies p ON p.id = r.policy_id
		WHERE p.tenant_id = $1 AND ($2 = '' OR p.id = $2)
		ORDER BY r.policy_id, r.ordinal`, tenant, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]model.PolicyRule{}
	for rows.Next() {
		var pid string
		var r model.PolicyRule
		var when, then []byte
		if err := rows.Scan(&pid, &r.ID, &r.Name, &when, &then); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(when, &r.When); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(then, &r.Then); err != nil {
			return nil, err
		}
		out[pid] = append(out[pid], r)
	}
	return out, rows.Err()
}

// Policies is the tenant's policies in order, each with its live rules.
// Warden evaluates the ones enforcing or monitoring.
func (s *Store) Policies(ctx context.Context, tenant string) ([]model.Policy, error) {
	rows, _ := s.Config.Query(ctx, `SELECT `+policyCols+` FROM policies WHERE tenant_id = $1 ORDER BY ordinal`, tenant)
	ps, err := collect(rows, func(r pgx.Rows) (model.Policy, error) { return scanPolicy(r) })
	if err != nil {
		return nil, err
	}
	rules, err := liveRules(ctx, s.Config, tenant, "")
	if err != nil {
		return nil, err
	}
	for i := range ps {
		if rs := rules[ps[i].ID]; rs != nil {
			ps[i].Rules = rs
		}
	}
	return ps, nil
}

func scanDraft(r pgx.Row) (*PolicyDraft, error) {
	var d PolicyDraft
	var rules []byte
	var at time.Time
	err := r.Scan(&d.Name, &d.Description, &d.FailMode, &rules, &at, &d.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.UpdatedAt = at.UnixMilli()
	return &d, json.Unmarshal(rules, &d.Rules)
}

const draftCols = `name, description, fail_mode, rules, updated_at, updated_by`

// PolicyDrafts is every pending draft, by policy id.
func (s *Store) PolicyDrafts(ctx context.Context, tenant string) (map[string]*PolicyDraft, error) {
	rows, err := s.Config.Query(ctx, `SELECT policy_id, `+draftCols+` FROM policy_drafts WHERE policy_id IN (SELECT id FROM policies WHERE tenant_id = $1)`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*PolicyDraft{}
	for rows.Next() {
		var id string
		var d PolicyDraft
		var rules []byte
		var at time.Time
		if err := rows.Scan(&id, &d.Name, &d.Description, &d.FailMode, &rules, &at, &d.UpdatedBy); err != nil {
			return nil, err
		}
		d.UpdatedAt = at.UnixMilli()
		if err := json.Unmarshal(rules, &d.Rules); err != nil {
			return nil, err
		}
		out[id] = &d
	}
	return out, rows.Err()
}

// lockPolicy reads a policy, its live rules and its draft for update, and
// checks If-Match.
func lockPolicy(ctx context.Context, tx pgx.Tx, tenant, id, ifMatch string) (model.Policy, *PolicyDraft, error) {
	p, err := scanPolicy(tx.QueryRow(ctx, `SELECT `+policyCols+` FROM policies WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenant, id))
	if err != nil {
		return p, nil, err
	}
	rules, err := liveRules(ctx, tx, tenant, id)
	if err != nil {
		return p, nil, err
	}
	if rs := rules[id]; rs != nil {
		p.Rules = rs
	}
	d, err := scanDraft(tx.QueryRow(ctx, `SELECT `+draftCols+` FROM policy_drafts WHERE policy_id = $1`, id))
	if err != nil {
		return p, nil, err
	}
	if ifMatch != "" && ifMatch != policyETag(p, d) {
		return p, d, &StaleError{Current: NewPolicyView(p, d, nil)}
	}
	return p, d, nil
}

// putDraft stores c as the policy's draft, giving new rules their ids, and
// returns it as stored.
func putDraft(ctx context.Context, tx pgx.Tx, id, actor string, c PolicyContent) (*PolicyDraft, error) {
	c.Rules = withRuleIDs(c.Rules, func() string { return randomID("r") })
	rules, _ := json.Marshal(c.Rules)
	if _, err := tx.Exec(ctx, `
		INSERT INTO policy_drafts (policy_id, name, description, fail_mode, rules, updated_by) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (policy_id) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description, fail_mode = EXCLUDED.fail_mode,
		  rules = EXCLUDED.rules, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		id, c.Name, c.Description, c.FailMode, rules, actor); err != nil {
		return nil, err
	}
	return scanDraft(tx.QueryRow(ctx, `SELECT `+draftCols+` FROM policy_drafts WHERE policy_id = $1`, id))
}

// allPolicies is every policy in order with its live rules, for warnings.
func (s *Store) allPolicies(ctx context.Context, tenant string) []model.Policy {
	all, _ := s.Policies(ctx, tenant)
	return all
}

// CreatePolicy adds an unpublished policy, last in order, holding c as its
// draft. Warden skips it until it's published.
func (s *Store) CreatePolicy(ctx context.Context, tenant, actor string, c PolicyContent) (PolicyView, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return PolicyView{}, err
	}
	defer tx.Rollback(ctx)
	p, err := scanPolicy(tx.QueryRow(ctx, `
		INSERT INTO policies (id, tenant_id, ordinal, name, description, mode, fail_mode, version)
		VALUES ($1, $2, (SELECT coalesce(max(ordinal), 0) + 1 FROM policies WHERE tenant_id = $2), $3, $4, 'draft', $5, 0)
		RETURNING `+policyCols, randomID("p"), tenant, c.Name, c.Description, c.FailMode))
	if err != nil {
		return PolicyView{}, uniqueConflict(err)
	}
	d, err := putDraft(ctx, tx, p.ID, actor, c)
	if err != nil {
		return PolicyView{}, err
	}
	if err := audit(ctx, tx, tenant, actor, "Created policy", p.Name, "Policy", p.ID, nil, d.PolicyContent); err != nil {
		return PolicyView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PolicyView{}, err
	}
	return NewPolicyView(p, d, s.allPolicies(ctx, tenant)), nil
}

// SavePolicyDraft replaces a policy's pending draft. The live version is
// untouched.
func (s *Store) SavePolicyDraft(ctx context.Context, tenant, actor, id, ifMatch string, c PolicyContent) (PolicyView, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return PolicyView{}, err
	}
	defer tx.Rollback(ctx)
	p, was, err := lockPolicy(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return PolicyView{}, err
	}
	d, err := putDraft(ctx, tx, id, actor, c)
	if err != nil {
		return PolicyView{}, err
	}
	var before any
	if was != nil {
		before = was.PolicyContent
	}
	if err := audit(ctx, tx, tenant, actor, "Edited policy draft", p.Name, "Policy", id, before, d.PolicyContent); err != nil {
		return PolicyView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PolicyView{}, err
	}
	return NewPolicyView(p, d, s.allPolicies(ctx, tenant)), nil
}

// DiscardPolicyDraft drops a published policy's pending draft. An
// unpublished policy is only its draft: delete the policy instead.
func (s *Store) DiscardPolicyDraft(ctx context.Context, tenant, actor, id, ifMatch string) (PolicyView, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return PolicyView{}, err
	}
	defer tx.Rollback(ctx)
	p, d, err := lockPolicy(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return PolicyView{}, err
	}
	if d == nil {
		return PolicyView{}, ErrNotFound
	}
	if p.Version == 0 {
		return PolicyView{}, ErrPublish{errors.New("this policy was never published; delete the policy instead")}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM policy_drafts WHERE policy_id = $1`, id); err != nil {
		return PolicyView{}, err
	}
	if err := audit(ctx, tx, tenant, actor, "Discarded policy draft", p.Name, "Policy", id, d.PolicyContent, nil); err != nil {
		return PolicyView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PolicyView{}, err
	}
	return NewPolicyView(p, nil, s.allPolicies(ctx, tenant)), nil
}

// PlanPublish is what Publish would leave live, for a dry run, and the
// reroute conflicts it would have. It writes nothing.
func (s *Store) PlanPublish(ctx context.Context, tenant, id, ifMatch, mode, failMode string) (cur, next model.Policy, warnings []string, err error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return cur, next, nil, err
	}
	defer tx.Rollback(ctx)
	cur, d, err := lockPolicy(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return cur, next, nil, err
	}
	var dc *PolicyContent
	if d != nil {
		dc = &d.PolicyContent
	}
	next, _, err = planPublish(cur, dc, mode, failMode)
	if err != nil {
		return cur, next, nil, ErrPublish{err}
	}
	warnings = []string{}
	if next.Mode == "enforce" {
		warnings = append(warnings, PolicyWarnings(id, PolicyContent{Rules: next.Rules}, s.allPolicies(ctx, tenant))...)
	}
	return cur, next, warnings, nil
}

// PublishPolicy makes the draft (or just a new mode or fail mode) the
// policy's next version: live for Warden on its next reload, and kept in
// the version history.
func (s *Store) PublishPolicy(ctx context.Context, tenant, actor, id, ifMatch, mode, failMode string) (PolicyView, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return PolicyView{}, err
	}
	defer tx.Rollback(ctx)
	cur, d, err := lockPolicy(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return PolicyView{}, err
	}
	var dc *PolicyContent
	if d != nil {
		dc = &d.PolicyContent
	}
	next, action, err := planPublish(cur, dc, mode, failMode)
	if err != nil {
		return PolicyView{}, ErrPublish{err}
	}
	if err := goLive(ctx, tx, tenant, actor, next); err != nil {
		return PolicyView{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM policy_drafts WHERE policy_id = $1`, id); err != nil {
		return PolicyView{}, err
	}
	if err := audit(ctx, tx, tenant, actor, action, fmt.Sprintf("%s v%d", next.Name, next.Version), "Policy", id, cur, next); err != nil {
		return PolicyView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PolicyView{}, err
	}
	return NewPolicyView(next, nil, s.allPolicies(ctx, tenant)), nil
}

// goLive writes next as the policy's live row and rules, and its version
// history entry.
func goLive(ctx context.Context, tx pgx.Tx, tenant, actor string, next model.Policy) error {
	if _, err := tx.Exec(ctx, `
		UPDATE policies SET name = $3, description = $4, mode = $5, fail_mode = $6, version = $7
		WHERE tenant_id = $1 AND id = $2`, tenant, next.ID, next.Name, next.Description, next.Mode, next.FailMode, next.Version); err != nil {
		return uniqueConflict(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM policy_rules WHERE policy_id = $1`, next.ID); err != nil {
		return err
	}
	for i, r := range next.Rules {
		when, _ := json.Marshal(r.When)
		then, _ := json.Marshal(r.Then)
		if _, err := tx.Exec(ctx, `INSERT INTO policy_rules (policy_id, id, ordinal, name, "when", "then") VALUES ($1,$2,$3,$4,$5,$6)`,
			next.ID, r.ID, i+1, r.Name, when, then); err != nil {
			return err
		}
	}
	rules, _ := json.Marshal(next.Rules)
	_, err := tx.Exec(ctx, `
		INSERT INTO policy_versions (tenant_id, policy_id, version, name, description, mode, fail_mode, rules, published_at, published_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now(),$9)`,
		tenant, next.ID, next.Version, next.Name, next.Description, next.Mode, next.FailMode, rules, actor)
	return err
}

// RollbackPolicy publishes an earlier version's content and mode as the
// next version. A pending draft stays pending.
func (s *Store) RollbackPolicy(ctx context.Context, tenant, actor, id, ifMatch string, to int) (PolicyView, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return PolicyView{}, err
	}
	defer tx.Rollback(ctx)
	cur, d, err := lockPolicy(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return PolicyView{}, err
	}
	old, err := policyVersion(ctx, tx, tenant, id, to)
	if err != nil {
		return PolicyView{}, err
	}
	if to == cur.Version {
		return PolicyView{}, ErrPublish{fmt.Errorf("v%d is already live", to)}
	}
	next := cur
	next.Name, next.Description, next.Mode, next.FailMode, next.Rules = old.Name, old.Description, old.Mode, old.FailMode, old.Rules
	next.Version = cur.Version + 1
	if err := goLive(ctx, tx, tenant, actor, next); err != nil {
		return PolicyView{}, err
	}
	target := fmt.Sprintf("%s v%d → v%d (as v%d)", next.Name, cur.Version, to, next.Version)
	if err := audit(ctx, tx, tenant, actor, "Rolled back policy", target, "Policy", id, cur, next); err != nil {
		return PolicyView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PolicyView{}, err
	}
	return NewPolicyView(next, d, s.allPolicies(ctx, tenant)), nil
}

func policyVersion(ctx context.Context, q queryer, tenant, id string, version int) (PolicyVersion, error) {
	var v PolicyVersion
	var rules []byte
	var at *time.Time
	err := q.QueryRow(ctx, `
		SELECT version, name, description, mode, fail_mode, rules, published_at, published_by
		FROM policy_versions WHERE tenant_id = $1 AND policy_id = $2 AND version = $3`, tenant, id, version).
		Scan(&v.Version, &v.Name, &v.Description, &v.Mode, &v.FailMode, &rules, &at, &v.PublishedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if at != nil {
		ms := at.UnixMilli()
		v.PublishedAt = &ms
	}
	return v, json.Unmarshal(rules, &v.Rules)
}

// PolicyVersions is a policy's published versions, newest first. It
// outlives the policy: a deleted policy's history stays.
func (s *Store) PolicyVersions(ctx context.Context, tenant, id string) ([]PolicyVersion, error) {
	rows, _ := s.Config.Query(ctx, `SELECT version FROM policy_versions WHERE tenant_id = $1 AND policy_id = $2 ORDER BY version DESC`, tenant, id)
	nums, err := collect(rows, func(r pgx.Rows) (int, error) {
		var n int
		return n, r.Scan(&n)
	})
	if err != nil {
		return nil, err
	}
	if len(nums) == 0 {
		return nil, ErrNotFound
	}
	out := make([]PolicyVersion, len(nums))
	for i, n := range nums {
		if out[i], err = policyVersion(ctx, s.Config, tenant, id, n); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// AllPolicyVersions is every published version of the tenant's existing
// policies, by policy id, newest first: what a rollback could bring back.
// A deleted policy's history is left out; it can't be rolled back.
func (s *Store) AllPolicyVersions(ctx context.Context, tenant string) (map[string][]PolicyVersion, error) {
	rows, err := s.Config.Query(ctx, `
		SELECT v.policy_id, v.version, v.name, v.description, v.mode, v.fail_mode, v.rules, v.published_at, v.published_by
		FROM policy_versions v JOIN policies p ON p.id = v.policy_id AND p.tenant_id = v.tenant_id
		WHERE v.tenant_id = $1 ORDER BY v.policy_id, v.version DESC`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]PolicyVersion{}
	for rows.Next() {
		var id string
		var v PolicyVersion
		var rules []byte
		var at *time.Time
		if err := rows.Scan(&id, &v.Version, &v.Name, &v.Description, &v.Mode, &v.FailMode, &rules, &at, &v.PublishedBy); err != nil {
			return nil, err
		}
		if at != nil {
			ms := at.UnixMilli()
			v.PublishedAt = &ms
		}
		if err := json.Unmarshal(rules, &v.Rules); err != nil {
			return nil, err
		}
		out[id] = append(out[id], v)
	}
	return out, rows.Err()
}

// DeletePolicy removes a policy that isn't live: never published, or
// disabled. An enforcing or monitoring policy has to be disabled first. Its
// published versions stay in the history.
func (s *Store) DeletePolicy(ctx context.Context, tenant, actor, id, ifMatch string) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	p, _, err := lockPolicy(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return err
	}
	if p.Mode != "draft" && p.Mode != "disabled" {
		return ErrPublish{fmt.Errorf("%s is %s; disable it before deleting it", p.Name, modeWords(p.Mode))}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM policies WHERE tenant_id = $1 AND id = $2`, tenant, id); err != nil {
		return err
	}
	if err := audit(ctx, tx, tenant, actor, "Deleted policy", p.Name, "Policy", id, p, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func modeWords(mode string) string {
	if mode == "monitor" {
		return "in monitor mode"
	}
	return "enforcing"
}
