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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/model"
)

// RuleContent is what an author writes. Mode and version come from publishing.
type RuleContent struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	FailMode    string         `json:"failMode"`
	When        []model.Cond   `json:"when"`
	Then        []model.Action `json:"then"`
}

// RuleEnv is what a rule may refer to: the engine's entity detectors, the
// catalog models and backend regions a "route to" can target, and the
// project ids a project condition names (nil: not checked).
type RuleEnv struct {
	Entities, Models, Regions, Projects []string
}

var ruleName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// RuleFields are what "is" and "is not" compare, as the engine reads them.
var RuleFields = []string{"team", "project", "key", "model", "provider", "header x-data-region"}

// ValidateRule checks a rule against what the engine can evaluate. The
// engine applies a rule's first action only, so a rule has exactly one.
func ValidateRule(r RuleContent, env RuleEnv) error {
	switch {
	case !ruleName.MatchString(r.Name):
		return errors.New("name must be lowercase letters, digits and dashes")
	case r.FailMode != "open" && r.FailMode != "closed":
		return errors.New("failMode must be open or closed")
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
	if len(r.Then) != 1 {
		return errors.New("a rule needs exactly one action")
	}
	switch a := r.Then[0]; a.Action {
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
	return nil
}

// planPublish is the rule a publish would leave live. With a draft, its
// content becomes the next version; mode and failMode, when set, change too.
// A rule's first publish defaults to monitor mode (§7.5.7); later ones keep
// the live mode.
func planPublish(cur model.PolicyRule, draft *RuleContent, mode, failMode string) (model.PolicyRule, string, error) {
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
		next.Name, next.Description, next.FailMode, next.When, next.Then = draft.Name, draft.Description, draft.FailMode, draft.When, draft.Then
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
		return "Published rule in monitor mode"
	case "disabled":
		return "Disabled rule"
	}
	return "Published rule"
}

// ErrPublish is a publish that can't go ahead as asked (planPublish's reasons).
type ErrPublish struct{ error }

// RuleDraft is a pending edit to a rule, not yet live.
type RuleDraft struct {
	RuleContent
	UpdatedAt int64  `json:"updatedAt"` // epoch ms
	UpdatedBy string `json:"updatedBy"`
}

// RuleView is a rule as the API shows it: the live version, any pending
// draft, and the ETag that covers both.
type RuleView struct {
	model.PolicyRule
	Draft *RuleDraft `json:"draft"`
	ETag  string     `json:"etag"`
}

func ruleETag(r model.PolicyRule, d *RuleDraft) string {
	var dc *RuleContent
	if d != nil {
		dc = &d.RuleContent
	}
	return ETag([]any{r.ID, r.Name, r.Description, r.Mode, r.FailMode, r.Version, r.When, r.Then, dc})
}

// RuleVersion is one published version of a rule.
type RuleVersion struct {
	Version int `json:"version"`
	RuleContent
	Mode        string  `json:"mode"`
	PublishedAt *int64  `json:"publishedAt"` // null: from before versions were kept
	PublishedBy *string `json:"publishedBy"`
}

const ruleCols = `id, ordinal, name, description, mode, fail_mode, version, "when", "then"`

func scanRule(r pgx.Row) (model.PolicyRule, error) {
	var p model.PolicyRule
	var when, then []byte
	err := r.Scan(&p.ID, &p.Ordinal, &p.Name, &p.Description, &p.Mode, &p.FailMode, &p.Version, &when, &then)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(when, &p.When); err != nil {
		return p, err
	}
	return p, json.Unmarshal(then, &p.Then)
}

func scanDraft(r pgx.Row) (*RuleDraft, error) {
	var d RuleDraft
	var when, then []byte
	var at time.Time
	err := r.Scan(&d.Name, &d.Description, &d.FailMode, &when, &then, &at, &d.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.UpdatedAt = at.UnixMilli()
	if err := json.Unmarshal(when, &d.When); err != nil {
		return nil, err
	}
	return &d, json.Unmarshal(then, &d.Then)
}

const draftCols = `name, description, fail_mode, "when", "then", updated_at, updated_by`

// RuleDrafts is every pending draft, by rule id.
func (s *Store) RuleDrafts(ctx context.Context, tenant string) (map[string]*RuleDraft, error) {
	rows, err := s.Config.Query(ctx, `SELECT rule_id, `+draftCols+` FROM policy_rule_drafts WHERE rule_id IN (SELECT id FROM policy_rules WHERE tenant_id = $1)`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*RuleDraft{}
	for rows.Next() {
		var id string
		var d RuleDraft
		var when, then []byte
		var at time.Time
		if err := rows.Scan(&id, &d.Name, &d.Description, &d.FailMode, &when, &then, &at, &d.UpdatedBy); err != nil {
			return nil, err
		}
		d.UpdatedAt = at.UnixMilli()
		if err := json.Unmarshal(when, &d.When); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(then, &d.Then); err != nil {
			return nil, err
		}
		out[id] = &d
	}
	return out, rows.Err()
}

// RuleView pairs a rule with its draft and ETag.
func NewRuleView(r model.PolicyRule, d *RuleDraft) RuleView {
	return RuleView{PolicyRule: r, Draft: d, ETag: ruleETag(r, d)}
}

// lockRule reads a rule and its draft for update, and checks If-Match.
func lockRule(ctx context.Context, tx pgx.Tx, tenant, id, ifMatch string) (model.PolicyRule, *RuleDraft, error) {
	r, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleCols+` FROM policy_rules WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenant, id))
	if err != nil {
		return r, nil, err
	}
	d, err := scanDraft(tx.QueryRow(ctx, `SELECT `+draftCols+` FROM policy_rule_drafts WHERE rule_id = $1`, id))
	if err != nil {
		return r, nil, err
	}
	if ifMatch != "" && ifMatch != ruleETag(r, d) {
		return r, d, &StaleError{Current: NewRuleView(r, d)}
	}
	return r, d, nil
}

func putDraft(ctx context.Context, tx pgx.Tx, id, actor string, c RuleContent) error {
	when, _ := json.Marshal(c.When)
	then, _ := json.Marshal(c.Then)
	_, err := tx.Exec(ctx, `
		INSERT INTO policy_rule_drafts (rule_id, name, description, fail_mode, "when", "then", updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (rule_id) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description, fail_mode = EXCLUDED.fail_mode,
		  "when" = EXCLUDED."when", "then" = EXCLUDED."then", updated_at = now(), updated_by = EXCLUDED.updated_by`,
		id, c.Name, c.Description, c.FailMode, when, then, actor)
	return err
}

// CreateRule adds an unpublished rule, last in order, holding c as its draft.
// Warden skips it until it's published.
func (s *Store) CreateRule(ctx context.Context, tenant, actor string, c RuleContent) (RuleView, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return RuleView{}, err
	}
	defer tx.Rollback(ctx)
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	when, _ := json.Marshal(c.When)
	then, _ := json.Marshal(c.Then)
	r, err := scanRule(tx.QueryRow(ctx, `
		INSERT INTO policy_rules (id, tenant_id, ordinal, name, description, mode, fail_mode, version, "when", "then")
		VALUES ($1, $2, (SELECT coalesce(max(ordinal), 0) + 1 FROM policy_rules WHERE tenant_id = $2), $3, $4, 'draft', $5, 0, $6, $7)
		RETURNING `+ruleCols, "r"+hex.EncodeToString(b), tenant, c.Name, c.Description, c.FailMode, when, then))
	if err != nil {
		return RuleView{}, uniqueConflict(err)
	}
	if err := putDraft(ctx, tx, r.ID, actor, c); err != nil {
		return RuleView{}, err
	}
	if err := audit(ctx, tx, tenant, actor, "Created rule", r.Name, "Policy", r.ID, nil, c); err != nil {
		return RuleView{}, err
	}
	d, err := scanDraft(tx.QueryRow(ctx, `SELECT `+draftCols+` FROM policy_rule_drafts WHERE rule_id = $1`, r.ID))
	if err != nil {
		return RuleView{}, err
	}
	return NewRuleView(r, d), tx.Commit(ctx)
}

// SaveDraft replaces a rule's pending draft. The live version is untouched.
func (s *Store) SaveDraft(ctx context.Context, tenant, actor, id, ifMatch string, c RuleContent) (RuleView, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return RuleView{}, err
	}
	defer tx.Rollback(ctx)
	r, was, err := lockRule(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return RuleView{}, err
	}
	if err := putDraft(ctx, tx, id, actor, c); err != nil {
		return RuleView{}, err
	}
	var before any
	if was != nil {
		before = was.RuleContent
	}
	if err := audit(ctx, tx, tenant, actor, "Edited rule draft", r.Name, "Policy", id, before, c); err != nil {
		return RuleView{}, err
	}
	d, err := scanDraft(tx.QueryRow(ctx, `SELECT `+draftCols+` FROM policy_rule_drafts WHERE rule_id = $1`, id))
	if err != nil {
		return RuleView{}, err
	}
	return NewRuleView(r, d), tx.Commit(ctx)
}

// DiscardDraft drops a published rule's pending draft. An unpublished rule
// is only its draft: delete the rule instead.
func (s *Store) DiscardDraft(ctx context.Context, tenant, actor, id, ifMatch string) (RuleView, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return RuleView{}, err
	}
	defer tx.Rollback(ctx)
	r, d, err := lockRule(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return RuleView{}, err
	}
	if d == nil {
		return RuleView{}, ErrNotFound
	}
	if r.Version == 0 {
		return RuleView{}, ErrPublish{errors.New("this rule was never published; delete the rule instead")}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM policy_rule_drafts WHERE rule_id = $1`, id); err != nil {
		return RuleView{}, err
	}
	if err := audit(ctx, tx, tenant, actor, "Discarded rule draft", r.Name, "Policy", id, d.RuleContent, nil); err != nil {
		return RuleView{}, err
	}
	return NewRuleView(r, nil), tx.Commit(ctx)
}

// PlanPublish is what Publish would leave live, for a dry run. It writes
// nothing.
func (s *Store) PlanPublish(ctx context.Context, tenant, id, ifMatch, mode, failMode string) (cur, next model.PolicyRule, err error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return cur, next, err
	}
	defer tx.Rollback(ctx)
	cur, d, err := lockRule(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return cur, next, err
	}
	var dc *RuleContent
	if d != nil {
		dc = &d.RuleContent
	}
	next, _, err = planPublish(cur, dc, mode, failMode)
	if err != nil {
		return cur, next, ErrPublish{err}
	}
	return cur, next, nil
}

// Publish makes the draft (or just a new mode or fail mode) the rule's next
// version: live for Warden on its next reload, and kept in the version
// history.
func (s *Store) Publish(ctx context.Context, tenant, actor, id, ifMatch, mode, failMode string) (RuleView, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return RuleView{}, err
	}
	defer tx.Rollback(ctx)
	cur, d, err := lockRule(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return RuleView{}, err
	}
	var dc *RuleContent
	if d != nil {
		dc = &d.RuleContent
	}
	next, action, err := planPublish(cur, dc, mode, failMode)
	if err != nil {
		return RuleView{}, ErrPublish{err}
	}
	if err := goLive(ctx, tx, tenant, actor, next); err != nil {
		return RuleView{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM policy_rule_drafts WHERE rule_id = $1`, id); err != nil {
		return RuleView{}, err
	}
	if err := audit(ctx, tx, tenant, actor, action, fmt.Sprintf("%s v%d", next.Name, next.Version), "Policy", id, cur, next); err != nil {
		return RuleView{}, err
	}
	return NewRuleView(next, nil), tx.Commit(ctx)
}

// goLive writes next as the rule's live row and its version history entry.
func goLive(ctx context.Context, tx pgx.Tx, tenant, actor string, next model.PolicyRule) error {
	when, _ := json.Marshal(next.When)
	then, _ := json.Marshal(next.Then)
	if _, err := tx.Exec(ctx, `
		UPDATE policy_rules SET name = $3, description = $4, mode = $5, fail_mode = $6, version = $7, "when" = $8, "then" = $9
		WHERE tenant_id = $1 AND id = $2`, tenant, next.ID, next.Name, next.Description, next.Mode, next.FailMode, next.Version, when, then); err != nil {
		return uniqueConflict(err)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO policy_rule_versions (tenant_id, rule_id, version, name, description, mode, fail_mode, "when", "then", published_at, published_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now(),$10)`,
		tenant, next.ID, next.Version, next.Name, next.Description, next.Mode, next.FailMode, when, then, actor)
	return err
}

// Rollback publishes an earlier version's content and mode as the next
// version. A pending draft stays pending.
func (s *Store) Rollback(ctx context.Context, tenant, actor, id, ifMatch string, to int) (RuleView, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return RuleView{}, err
	}
	defer tx.Rollback(ctx)
	cur, d, err := lockRule(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return RuleView{}, err
	}
	old, err := ruleVersion(ctx, tx, tenant, id, to)
	if err != nil {
		return RuleView{}, err
	}
	if to == cur.Version {
		return RuleView{}, ErrPublish{fmt.Errorf("v%d is already live", to)}
	}
	next := cur
	next.Name, next.Description, next.Mode, next.FailMode, next.When, next.Then = old.Name, old.Description, old.Mode, old.FailMode, old.When, old.Then
	next.Version = cur.Version + 1
	if err := goLive(ctx, tx, tenant, actor, next); err != nil {
		return RuleView{}, err
	}
	target := fmt.Sprintf("%s v%d → v%d (as v%d)", next.Name, cur.Version, to, next.Version)
	if err := audit(ctx, tx, tenant, actor, "Rolled back rule", target, "Policy", id, cur, next); err != nil {
		return RuleView{}, err
	}
	return NewRuleView(next, d), tx.Commit(ctx)
}

func ruleVersion(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenant, id string, version int) (RuleVersion, error) {
	var v RuleVersion
	var when, then []byte
	var at *time.Time
	err := q.QueryRow(ctx, `
		SELECT version, name, description, mode, fail_mode, "when", "then", published_at, published_by
		FROM policy_rule_versions WHERE tenant_id = $1 AND rule_id = $2 AND version = $3`, tenant, id, version).
		Scan(&v.Version, &v.Name, &v.Description, &v.Mode, &v.FailMode, &when, &then, &at, &v.PublishedBy)
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
	if err := json.Unmarshal(when, &v.When); err != nil {
		return v, err
	}
	return v, json.Unmarshal(then, &v.Then)
}

// RuleVersions is a rule's published versions, newest first. It outlives the
// rule: a deleted rule's history stays.
func (s *Store) RuleVersions(ctx context.Context, tenant, id string) ([]RuleVersion, error) {
	rows, _ := s.Config.Query(ctx, `SELECT version FROM policy_rule_versions WHERE tenant_id = $1 AND rule_id = $2 ORDER BY version DESC`, tenant, id)
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
	out := make([]RuleVersion, len(nums))
	for i, n := range nums {
		if out[i], err = ruleVersion(ctx, s.Config, tenant, id, n); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DeleteRule removes a rule that isn't live: never published, or disabled.
// An enforcing or monitoring rule has to be disabled first. Its published
// versions stay in the history.
func (s *Store) DeleteRule(ctx context.Context, tenant, actor, id, ifMatch string) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	r, _, err := lockRule(ctx, tx, tenant, id, ifMatch)
	if err != nil {
		return err
	}
	if r.Mode != "draft" && r.Mode != "disabled" {
		return ErrPublish{fmt.Errorf("%s is %s; disable it before deleting it", r.Name, modeWords(r.Mode))}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM policy_rules WHERE tenant_id = $1 AND id = $2`, tenant, id); err != nil {
		return err
	}
	if err := audit(ctx, tx, tenant, actor, "Deleted rule", r.Name, "Policy", id, r, nil); err != nil {
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
