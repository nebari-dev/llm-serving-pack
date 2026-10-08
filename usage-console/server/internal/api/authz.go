package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/auth"
)

// Who may do what (backend-decisions §7, decided 2026-10-05). Roles come from
// the caller's Keycloak groups; owner may do everything and isn't listed.

// Action is a kind of write the role table governs; the console reads which
// ones the caller may do from GET /session.
type Action string

const (
	ActRead         Action = "read"
	ActDraftRules   Action = "rules.draft"
	ActPublishRules Action = "rules.publish"
	ActKillSwitch   Action = "killswitch"
	ActCapture      Action = "capture"
	ActPrices       Action = "prices"
	ActBudgets      Action = "budgets"
	ActRouting      Action = "routing"
	ActProjects     Action = "projects"
	ActDetectors    Action = "detectors"
	// ActOwnKeys is any role; a key's later writes also need its owner, or
	// ActAnyKeys.
	ActOwnKeys Action = "keys.own"
	ActAnyKeys Action = "keys.any"
	// ActMembers is assigning roles, which happens in Keycloak.
	ActMembers Action = "members"
	// actSignedIn is GET /session: no role needed, so a signed-in user with
	// none can be told why they see nothing.
	actSignedIn Action = "signed-in"
)

type rule struct {
	// roles may do it besides owner; nil is every role.
	roles []auth.Role
	// what is the action, for "Needs role … <what>".
	what string
}

var actionRules = map[Action]rule{
	ActRead:         {nil, "to read"},
	ActDraftRules:   {[]auth.Role{auth.Security, auth.Editor}, "to draft rules"},
	ActPublishRules: {[]auth.Role{auth.Admin, auth.Security}, "to publish, reorder, roll back or delete policies"},
	ActKillSwitch:   {[]auth.Role{auth.Admin, auth.Security}, "to use the kill switch"},
	ActCapture:      {[]auth.Role{auth.Admin, auth.Security}, "to turn content capture on or off, or reveal captured content"},
	ActPrices:       {[]auth.Role{auth.Admin}, "to change prices"},
	ActBudgets:      {[]auth.Role{auth.Admin, auth.Finance}, "to change budgets"},
	ActRouting:      {[]auth.Role{auth.Admin, auth.Editor}, "to change aliases, routing and providers"},
	ActProjects:     {[]auth.Role{auth.Admin, auth.Finance, auth.Editor}, "to change projects"},
	ActDetectors:    {[]auth.Role{auth.Admin, auth.Security}, "to change custom entities or review detector hits"},
	ActOwnKeys:      {nil, "to create keys"},
	ActAnyKeys:      {[]auth.Role{auth.Admin}, "to change someone else's key"},
	ActMembers:      {[]auth.Role{}, "to assign roles"},
}

// routeActions is every write's action, by method and path under
// /api/v1/{tenant}. GETs are ActRead unless listed. A write missing here is
// owner-only (TestEveryWriteHasARole catches it).
var routeActions = map[string]Action{
	"GET /session": actSignedIn,

	"POST /projects":        ActProjects,
	"PUT /projects/{id}":    ActProjects,
	"DELETE /projects/{id}": ActProjects,

	"PUT /aliases/{alias}":       ActRouting,
	"DELETE /aliases/{alias}":    ActRouting,
	"POST /backends":             ActRouting,
	"POST /backends/test":        ActRouting,
	"PUT /backends/{name}":       ActRouting,
	"DELETE /backends/{name}":    ActRouting,
	"PUT /backends/{name}/key":   ActRouting,
	"POST /backends/{name}/test": ActRouting,
	"POST /routes":               ActRouting,
	"PUT /routes/{name}":         ActRouting,
	"DELETE /routes/{name}":      ActRouting,
	"PUT /routes/{name}/capture": ActCapture,
	// The catalog is every tenant's, like prices.
	"PUT /models/{id}/family": ActPrices,
	"POST /routing/apply":     ActRouting,

	"POST /pricing/sync":                     ActPrices,
	"POST /pricing/proposals/{id}/accept":    ActPrices,
	"POST /pricing/proposals/{id}/dismiss":   ActPrices,
	"POST /pricing/{model}/{backend}":        ActPrices,
	"DELETE /pricing/{model}/{backend}":      ActPrices,
	"PUT /pricing/{model}/{backend}/source":  ActPrices,
	"DELETE /pricing/{model}/{backend}/{at}": ActPrices,

	"POST /keys":                      ActOwnKeys,
	"POST /keys/{id}/revoke":          ActOwnKeys,
	"POST /keys/{id}/rotate":          ActOwnKeys,
	"POST /keys/{id}/rotation/extend": ActOwnKeys,
	"POST /keys/{id}/rotation/finish": ActOwnKeys,

	"POST /budgets":        ActBudgets,
	"PATCH /budgets/{id}":  ActBudgets,
	"DELETE /budgets/{id}": ActBudgets,

	// A policy's rules are drafted and published with it (§5.2).
	"POST /policies":               ActDraftRules,
	"PUT /policies/{id}/draft":     ActDraftRules,
	"DELETE /policies/{id}/draft":  ActDraftRules,
	"PUT /policies/order":          ActPublishRules,
	"POST /policies/{id}/publish":  ActPublishRules,
	"POST /policies/{id}/rollback": ActPublishRules,
	// Replay writes nothing and shows no content: counts and receipt links.
	"POST /policies/{id}/replay": ActRead,
	// Deleting a policy stops it being enforced, like a publish.
	"DELETE /policies/{id}": ActPublishRules,

	"POST /warden/passthrough": ActKillSwitch,
	// Content is captured only where an elevated role turned capture on
	// (spec §9.2); reading it takes the same roles.
	"POST /receipts/{id}/reveal": ActCapture,
	// Custom entities change what's redacted, as publishing a rule does;
	// judging hits is a security review.
	"POST /entities":               ActDetectors,
	"PUT /entities/{id}":           ActDetectors,
	"DELETE /entities/{id}":        ActDetectors,
	"POST /detectors/hits/verdict": ActDetectors,
	// Exports are reads of what the caller can already see (audited).
	"POST /receipts/export":      ActRead,
	"POST /receipts/export.csv":  ActRead,
	"POST /receipts/{id}/export": ActRead,
	// Sends a test request with the caller's own gateway key.
	"POST /gateway/test": ActRead,
}

// allowed is whether roles may do act.
func allowed(act Action, roles []auth.Role) bool {
	if act == actSignedIn {
		return true
	}
	if slices.Contains(roles, auth.Owner) {
		return true
	}
	r, ok := actionRules[act]
	if !ok {
		return false // unknown: owner only
	}
	if r.roles == nil {
		return len(roles) > 0
	}
	for _, x := range roles {
		if slices.Contains(r.roles, x) {
			return true
		}
	}
	return false
}

// forbidden is a write the caller's roles don't allow (403, spec §7.6): it
// names the roles that may, and who changes roles.
type forbidden struct {
	msg   string
	roles []auth.Role
}

func (f forbidden) Error() string { return f.msg }

func roleNames(rs []auth.Role) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}

func orList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " or " + xs[len(xs)-1]
}

// needs is the refusal for act: "Needs role X or Y to …".
func needs(act Action, u auth.User) forbidden {
	r := actionRules[act]
	if r.roles == nil {
		// Every role may: the caller has none.
		return forbidden{
			msg:   "Needs a Stargate role (viewer or above): " + u.Email + " isn't in any Stargate group. An owner adds people to groups in Keycloak.",
			roles: auth.Roles[1:],
		}
	}
	if len(r.roles) == 0 {
		return forbidden{msg: "Needs role owner " + r.what + ".", roles: r.roles}
	}
	have := "no role"
	if len(u.Roles) > 0 {
		have = strings.Join(roleNames(u.Roles), ", ")
	}
	return forbidden{
		msg:   "Needs role " + orList(roleNames(r.roles)) + " " + r.what + " (you have " + have + "). An owner assigns roles in Keycloak.",
		roles: r.roles,
	}
}

// ---- who's asking ---------------------------------------------------------

type userKey struct{}

func withUser(ctx context.Context, u auth.User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

func userFrom(ctx context.Context) (auth.User, bool) {
	u, ok := ctx.Value(userKey{}).(auth.User)
	return u, ok
}

// actor is who r acts as: the audit row's actor, a new key's owner.
func actor(r *http.Request) string {
	if u, ok := userFrom(r.Context()); ok {
		return u.Email
	}
	// Every handler runs inside wrap, which sets the user; reaching here is
	// a bug, and an audit row shouldn't name anyone it can't vouch for.
	panic("api: no user on the request (handler not wrapped)")
}

// DevUser is who every caller is with no IdP configured.
var DevUser = auth.User{Email: auth.DevUserEmail, Roles: []auth.Role{auth.Owner}}

func (s *Server) devUser() auth.User {
	if s.Dev.Email == "" {
		return DevUser
	}
	return s.Dev
}

type unauthenticated struct{ error }

// authenticate is who r acts as: the signed-in user, or the dev user when
// no IdP is configured.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (auth.User, error) {
	if s.Auth == nil {
		return s.devUser(), nil
	}
	u, err := s.Auth.Authenticate(w, r)
	if err != nil {
		return auth.User{}, unauthenticated{err}
	}
	return u, nil
}

// authorize checks u may call pattern (a mux pattern under /api/v1/{tenant}).
// Key writes after creation also need the key's owner, or ActAnyKeys.
func (s *Server) authorize(r *http.Request, tenant string, u auth.User, pattern string) error {
	method, path, _ := strings.Cut(pattern, " ")
	act, ok := routeActions[method+" "+strings.TrimPrefix(path, "/api/v1/{tenant}")]
	if !ok && method == http.MethodGet {
		act, ok = ActRead, true
	}
	if !ok {
		log.Printf("authz: %s has no role entry; owner only", pattern)
	}
	if !allowed(act, u.Roles) {
		if !ok {
			return forbidden{msg: "Needs role owner (this write has no entry in the role table).", roles: []auth.Role{}}
		}
		return needs(act, u)
	}
	if act == ActOwnKeys && r.PathValue("id") != "" && !allowed(ActAnyKeys, u.Roles) {
		owner, err := s.ownerOf(r.Context(), tenant, r.PathValue("id"))
		if err != nil {
			return err
		}
		if owner != u.Email {
			return forbidden{
				msg:   "Needs role admin to change someone else's key: this one belongs to " + owner + ".",
				roles: actionRules[ActAnyKeys].roles,
			}
		}
	}
	return nil
}

func (s *Server) ownerOf(ctx context.Context, tenant, id string) (string, error) {
	if s.keyOwner != nil {
		return s.keyOwner(ctx, tenant, id)
	}
	return s.Store.KeyOwner(ctx, tenant, id)
}

// touchEvery is how often a member's row is rewritten when nothing about
// them changed (last seen).
const touchEvery = time.Hour

type touched struct {
	roles string
	at    time.Time
}

// touchUser records u in the members cache (users) when they or their roles
// changed, or hourly. The cache is what Settings → Members lists; the token
// stays the source of roles.
func (s *Server) touchUser(ctx context.Context, tenant string, u auth.User) {
	if s.Store == nil {
		return
	}
	key := tenant + "\x00" + u.Email
	roles := strings.Join(roleNames(u.Roles), ",") + "\x00" + u.Name
	if v, ok := s.seen.Load(key); ok {
		if t := v.(touched); t.roles == roles && time.Since(t.at) < touchEvery {
			return
		}
	}
	if err := s.Store.TouchUser(ctx, tenant, u.Email, u.Name, roleNames(u.Roles)); err != nil {
		log.Printf("record member %s: %v", u.Email, err)
		return
	}
	s.seen.Store(key, touched{roles, time.Now()})
}

// ---- GET /session's auth parts --------------------------------------------

type SessionAuth struct {
	// Mode is "oidc" when people sign in, "dev" when everyone is the dev user.
	Mode string `json:"mode"`
	// SignInURL and SignOutURL are this control plane's (empty in dev mode).
	SignInURL  string `json:"signInUrl,omitempty"`
	SignOutURL string `json:"signOutUrl,omitempty"`
	// AccountURL is the user's Keycloak account page; GroupsURL the realm's
	// groups in Keycloak's admin console, where roles are assigned.
	AccountURL string `json:"accountUrl,omitempty"`
	GroupsURL  string `json:"groupsUrl,omitempty"`
	// GroupPrefix: the Keycloak group for role r is GroupPrefix+r.
	GroupPrefix string `json:"groupPrefix,omitempty"`
}

// Permission is whether the caller may do an action, and who may.
type Permission struct {
	Allowed bool `json:"allowed"`
	// Roles may do it besides owner; empty for owner only.
	Roles []string `json:"roles"`
}

func (s *Server) sessionAuth(u auth.User) (SessionActor, SessionAuth, map[Action]Permission) {
	a := SessionActor{Email: u.Email, Name: u.Name, Roles: roleNames(u.Roles), Authenticated: s.Auth != nil}
	if len(u.Roles) > 0 {
		a.Role = string(u.Roles[0])
	}
	m := SessionAuth{Mode: "dev"}
	if s.Auth != nil {
		m = SessionAuth{Mode: "oidc", SignInURL: "/api/auth/login", SignOutURL: "/api/auth/logout", GroupPrefix: s.Auth.GroupPrefix}
		if base, realm, ok := strings.Cut(s.Auth.Issuer, "/realms/"); ok {
			m.AccountURL = s.Auth.Issuer + "/account"
			m.GroupsURL = base + "/admin/master/console/#/" + realm + "/groups"
		}
	}
	perms := map[Action]Permission{}
	for act, r := range actionRules {
		roles := r.roles
		if roles == nil {
			roles = auth.Roles[1:]
		}
		perms[act] = Permission{Allowed: allowed(act, u.Roles), Roles: roleNames(roles)}
	}
	return a, m, perms
}

// writeAuthError answers a request auth refused.
func writeAuthError(w http.ResponseWriter, err error) bool {
	var f forbidden
	var un unauthenticated
	switch {
	case errors.As(err, &un) && errors.Is(un.error, auth.ErrCrossSite):
		writeJSON(w, 403, errBody("cross_site", "Writes must come from the console's own origin."))
	case errors.As(err, &un):
		body := errBody("unauthenticated", "Sign in to use the console.")
		body["error"].(map[string]any)["loginUrl"] = "/api/auth/login"
		writeJSON(w, 401, body)
	case errors.As(err, &f):
		body := errBody("forbidden", f.msg)
		body["error"].(map[string]any)["roles"] = roleNames(f.roles)
		writeJSON(w, 403, body)
	default:
		return false
	}
	return true
}
