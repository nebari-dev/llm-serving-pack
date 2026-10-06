package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/auth"
	"github.com/jbouder/stargate/server/internal/auth/authtest"
	"github.com/jbouder/stargate/server/internal/store"
)

// serve runs one request and reports its status, or 0 if the handler got
// past auth and then panicked on the nil Store (these tests have no
// database: reaching the Store means the request was let through).
func serve(h http.Handler, r *http.Request) (code int, body string) {
	w := httptest.NewRecorder()
	func() {
		defer func() {
			if recover() != nil {
				code = 0
			}
		}()
		h.ServeHTTP(w, r)
		code = w.Code
	}()
	return code, w.Body.String()
}

// The decision table (backend-decisions §7), restated here rather than read
// from the code, so a change to either shows up. owner may do everything and
// isn't listed.
var roleTable = []struct {
	method, path string
	roles        []auth.Role // nil: every role
}{
	{"GET", "/keys", nil},
	{"GET", "/rules", nil},
	{"GET", "/members", nil},
	{"POST", "/projects", []auth.Role{auth.Admin, auth.Finance, auth.Editor}},
	{"PUT", "/projects/p1", []auth.Role{auth.Admin, auth.Finance, auth.Editor}},
	{"DELETE", "/projects/p1", []auth.Role{auth.Admin, auth.Finance, auth.Editor}},
	{"PUT", "/aliases/fast", []auth.Role{auth.Admin, auth.Editor}},
	{"DELETE", "/aliases/fast", []auth.Role{auth.Admin, auth.Editor}},
	{"POST", "/pricing/sync", []auth.Role{auth.Admin}},
	{"POST", "/pricing/proposals/x/accept", []auth.Role{auth.Admin}},
	{"POST", "/pricing/proposals/x/dismiss", []auth.Role{auth.Admin}},
	{"POST", "/pricing/gpt-5/openai", []auth.Role{auth.Admin}},
	{"PUT", "/pricing/gpt-5/openai/source", []auth.Role{auth.Admin}},
	{"DELETE", "/pricing/gpt-5/openai/1700000000000", []auth.Role{auth.Admin}},
	{"POST", "/backends", []auth.Role{auth.Admin, auth.Editor}},
	{"POST", "/backends/test", []auth.Role{auth.Admin, auth.Editor}},
	{"PUT", "/backends/b", []auth.Role{auth.Admin, auth.Editor}},
	{"DELETE", "/backends/b", []auth.Role{auth.Admin, auth.Editor}},
	{"PUT", "/backends/b/key", []auth.Role{auth.Admin, auth.Editor}},
	{"POST", "/backends/b/test", []auth.Role{auth.Admin, auth.Editor}},
	{"POST", "/routes", []auth.Role{auth.Admin, auth.Editor}},
	{"PUT", "/routes/r", []auth.Role{auth.Admin, auth.Editor}},
	{"DELETE", "/routes/r", []auth.Role{auth.Admin, auth.Editor}},
	{"POST", "/routing/apply", []auth.Role{auth.Admin, auth.Editor}},
	{"POST", "/keys", nil},
	{"POST", "/budgets", []auth.Role{auth.Admin, auth.Finance}},
	{"PATCH", "/budgets/b1", []auth.Role{auth.Admin, auth.Finance}},
	{"DELETE", "/budgets/b1", []auth.Role{auth.Admin, auth.Finance}},
	{"POST", "/rules", []auth.Role{auth.Security, auth.Editor}},
	{"PUT", "/rules/r1/draft", []auth.Role{auth.Security, auth.Editor}},
	{"DELETE", "/rules/r1/draft", []auth.Role{auth.Security, auth.Editor}},
	{"PUT", "/rules/order", []auth.Role{auth.Admin, auth.Security}},
	{"POST", "/rules/r1/publish", []auth.Role{auth.Admin, auth.Security}},
	{"POST", "/rules/r1/rollback", []auth.Role{auth.Admin, auth.Security}},
	{"DELETE", "/rules/r1", []auth.Role{auth.Admin, auth.Security}},
	{"POST", "/warden/passthrough", []auth.Role{auth.Admin, auth.Security}},
	{"POST", "/receipts/r1/reveal", []auth.Role{auth.Admin, auth.Security}},
	{"POST", "/receipts/export", nil},
	{"POST", "/receipts/r1/export", nil},
	{"POST", "/gateway/test", nil},
}

func TestRoleTable(t *testing.T) {
	for _, c := range roleTable {
		for _, role := range auth.Roles {
			s := &Server{Tenants: []string{"demo"}, Dev: auth.User{Email: "someone@example.com", Roles: []auth.Role{role}}}
			code, body := serve(s.Handler(), httptest.NewRequest(c.method, "/api/v1/demo"+c.path, strings.NewReader("{}")))
			want := role == auth.Owner || c.roles == nil || slices.Contains(c.roles, role)
			if got := code != http.StatusForbidden; got != want {
				t.Errorf("%s %s as %s: %d %s (allowed: %v)", c.method, c.path, role, code, body, want)
				continue
			}
			if !want {
				// §7.6: a denied write names the roles that may do it.
				var e struct {
					Error struct {
						Code  string   `json:"code"`
						Roles []string `json:"roles"`
					} `json:"error"`
				}
				json.Unmarshal([]byte(body), &e)
				wantRoles := make([]string, len(c.roles))
				for i, r := range c.roles {
					wantRoles[i] = string(r)
				}
				if e.Error.Code != "forbidden" || !slices.Equal(e.Error.Roles, wantRoles) || !strings.Contains(body, "Needs role") {
					t.Errorf("%s %s as %s: %s", c.method, c.path, role, body)
				}
			}
		}
	}
}

// Every write the API serves is in the role table: a route added without an
// entry would otherwise fall back to owner-only.
func TestEveryWriteHasARole(t *testing.T) {
	s := &Server{Tenants: []string{"demo"}}
	s.Handler()
	if len(s.patterns) == 0 {
		t.Fatal("no routes recorded")
	}
	for _, p := range s.patterns {
		method, path, _ := strings.Cut(p, " ")
		if method == "GET" {
			continue
		}
		if _, ok := routeActions[method+" "+strings.TrimPrefix(path, "/api/v1/{tenant}")]; !ok {
			t.Errorf("%s has no entry in routeActions", p)
		}
	}
}

// Own keys: their owner, whatever their role; anyone else's: admin.
func TestKeyWritesNeedTheOwnerOrAnAdmin(t *testing.T) {
	owners := map[string]string{"k1": "olive@example.com"}
	for _, c := range []struct {
		email string
		roles []auth.Role
		want  bool
	}{
		{"olive@example.com", []auth.Role{auth.Viewer}, true},
		{"vic@example.com", []auth.Role{auth.Viewer}, false},
		{"eddie@example.com", []auth.Role{auth.Editor, auth.Security, auth.Finance}, false},
		{"ada@example.com", []auth.Role{auth.Admin}, true},
		{"owen@example.com", []auth.Role{auth.Owner}, true},
	} {
		for _, path := range []string{"/keys/k1/revoke", "/keys/k1/rotate", "/keys/k1/rotation/extend", "/keys/k1/rotation/finish"} {
			s := &Server{Tenants: []string{"demo"}, Dev: auth.User{Email: c.email, Roles: c.roles}}
			s.keyOwner = func(_ context.Context, _, id string) (string, error) {
				if o, ok := owners[id]; ok {
					return o, nil
				}
				return "", store.ErrNotFound
			}
			code, body := serve(s.Handler(), httptest.NewRequest("POST", "/api/v1/demo"+path, strings.NewReader("{}")))
			if got := code != http.StatusForbidden; got != c.want {
				t.Errorf("%s %v on %s: %d %s", c.email, c.roles, path, code, body)
			}
			if code == http.StatusForbidden && !strings.Contains(body, "olive@example.com") {
				t.Errorf("refusal doesn't name the owner: %s", body)
			}
		}
	}
}

// Signed in, with no Stargate group: nothing to read, but /session answers
// (so the console can say why).
func TestNoRoleReadsNothing(t *testing.T) {
	s := &Server{Tenants: []string{"demo"}, Dev: auth.User{Email: "nobody@example.com", Roles: []auth.Role{}}}
	if code, body := serve(s.Handler(), httptest.NewRequest("GET", "/api/v1/demo/keys", nil)); code != http.StatusForbidden {
		t.Fatalf("GET /keys: %d %s", code, body)
	}
	if code, body := serve(s.Handler(), httptest.NewRequest("GET", "/api/v1/demo/session", nil)); code == http.StatusForbidden || code == http.StatusUnauthorized {
		t.Fatalf("GET /session: %d %s", code, body)
	}
}

// Dev mode is unchanged: with no IdP every caller is dev@localhost, owner,
// and needs no credentials.
func TestDevModeIsDevLocalhostOwner(t *testing.T) {
	s := &Server{Tenants: []string{"demo"}}
	u := s.devUser()
	if u.Email != "dev@localhost" || !slices.Equal(u.Roles, []auth.Role{auth.Owner}) {
		t.Fatalf("dev user %+v", u)
	}
	a, mode, perms := s.sessionAuth(u)
	if a.Email != "dev@localhost" || a.Role != "owner" || !slices.Equal(a.Roles, []string{"owner"}) || a.Authenticated || mode.Mode != "dev" {
		t.Fatalf("session actor %+v auth %+v", a, mode)
	}
	for act, p := range perms {
		if !p.Allowed {
			t.Errorf("dev owner may not %s", act)
		}
	}
	if perms[ActPublishRules].Roles[0] != "admin" || len(perms[ActPublishRules].Roles) != 2 {
		t.Errorf("publish roles %v", perms[ActPublishRules].Roles)
	}
	code, body := serve(s.Handler(), httptest.NewRequest("PUT", "/api/v1/demo/aliases/fast", strings.NewReader(`{"target":"x"}`)))
	if code != http.StatusPreconditionRequired {
		t.Fatalf("dev write: %d %s", code, body)
	}
}

func TestSessionPermissionsFollowRoles(t *testing.T) {
	s := &Server{Tenants: []string{"demo"}}
	_, _, perms := s.sessionAuth(auth.User{Email: "fran@example.com", Roles: []auth.Role{auth.Finance}})
	if !perms[ActBudgets].Allowed || perms[ActPrices].Allowed || perms[ActPublishRules].Allowed || !perms[ActOwnKeys].Allowed || perms[ActAnyKeys].Allowed {
		t.Fatalf("finance permissions %+v", perms)
	}
}

func TestOIDCModeNeedsSignIn(t *testing.T) {
	idp := authtest.New(t)
	s := &Server{Tenants: []string{"demo"}, Auth: idp.OIDC()}
	h := s.Handler()
	code, body := serve(h, httptest.NewRequest("GET", "/api/v1/demo/keys", nil))
	if code != http.StatusUnauthorized || !strings.Contains(body, `"unauthenticated"`) || !strings.Contains(body, "/api/auth/login") {
		t.Fatalf("anonymous: %d %s", code, body)
	}

	req := func(method, path, tok string) *http.Request {
		r := httptest.NewRequest(method, "/api/v1/demo"+path, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+tok)
		return r
	}
	vic := idp.Token(t, "vic@example.com", "Vic Viewer", "/stargate-viewer")
	if code, body := serve(h, req("POST", "/budgets", vic)); code != http.StatusForbidden {
		t.Fatalf("viewer creating a budget: %d %s", code, body)
	}
	fran := idp.Token(t, "fran@example.com", "Fran Finance", "/stargate-finance")
	if code, body := serve(h, req("POST", "/budgets", fran)); code == http.StatusForbidden || code == http.StatusUnauthorized {
		t.Fatalf("finance creating a budget: %d %s", code, body)
	}
	// The dev user is not a fallback once an IdP is configured.
	if code, _ := serve(h, httptest.NewRequest("POST", "/api/v1/demo/budgets", strings.NewReader("{}"))); code != http.StatusUnauthorized {
		t.Fatalf("anonymous write: %d", code)
	}
}

// What a handler records as the actor (the audit row's actor, a key's owner,
// an export's exportedBy) is the signed-in user, not dev@localhost.
func TestActorIsTheSignedInUser(t *testing.T) {
	idp := authtest.New(t)
	for _, c := range []struct {
		name string
		s    *Server
		tok  string
		want string
	}{
		{"oidc", &Server{Tenants: []string{"demo"}, Auth: idp.OIDC()}, idp.Token(t, "Ada@Example.com", "Ada", "/stargate-admin"), "ada@example.com"},
		{"dev", &Server{Tenants: []string{"demo"}}, "", "dev@localhost"},
		{"dev, named", &Server{Tenants: []string{"demo"}, Dev: auth.User{Email: "me@laptop", Roles: []auth.Role{auth.Owner}}}, "", "me@laptop"},
	} {
		var got string
		mux := http.NewServeMux()
		mux.Handle("POST /api/v1/{tenant}/budgets", c.s.wrap("POST /api/v1/{tenant}/budgets", func(_ http.ResponseWriter, r *http.Request, _ string) (any, error) {
			got = actor(r)
			return map[string]bool{"ok": true}, nil
		}))
		r := httptest.NewRequest("POST", "/api/v1/demo/budgets", nil)
		if c.tok != "" {
			r.Header.Set("Authorization", "Bearer "+c.tok)
		}
		if code, body := serve(mux, r); code != 200 || got != c.want {
			t.Errorf("%s: %d %s, actor %q", c.name, code, body, got)
		}
	}
}
