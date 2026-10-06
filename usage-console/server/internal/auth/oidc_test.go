package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeIdP is enough of Keycloak for the code flow: discovery, JWKS, and a
// token endpoint that checks the PKCE verifier and the client secret, and
// hands out ID tokens with the nonce it was given.
type fakeIdP struct {
	*testIdP
	issuer    string
	challenge string // from the last authorization request
	nonce     string
	refreshed int
	// tokenClaims lets a test change the ID token the endpoint returns.
	tokenClaims func(map[string]any)
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	f := &fakeIdP{testIdP: newTestIdP(t)}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.issuer = srv.URL + "/realms/nebari"
	mux.HandleFunc("GET /realms/nebari/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 f.issuer,
			"authorization_endpoint": f.issuer + "/protocol/openid-connect/auth",
			"token_endpoint":         f.issuer + "/protocol/openid-connect/token",
			"end_session_endpoint":   f.issuer + "/protocol/openid-connect/logout",
			"jwks_uri":               f.srv.URL,
		})
	})
	mux.HandleFunc("POST /realms/nebari/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if id, secret, _ := r.BasicAuth(); id != testAudience || secret != "s3cret" {
			http.Error(w, `{"error":"unauthorized_client"}`, 401)
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if r.PostForm.Get("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
		case "refresh_token":
			if r.PostForm.Get("refresh_token") != "the-refresh" {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			f.refreshed++
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, 400)
			return
		}
		c := f.claims(time.Now())
		c["nonce"] = f.nonce
		if f.tokenClaims != nil {
			f.tokenClaims(c)
		}
		json.NewEncoder(w).Encode(map[string]any{"id_token": f.token(t, c), "refresh_token": "the-refresh", "access_token": "opaque"})
	})
	return f
}

func (f *fakeIdP) claims(now time.Time) map[string]any {
	c := claims(now)
	c["iss"] = f.issuer
	return c
}

func (f *fakeIdP) oidc(t *testing.T) *OIDC {
	t.Helper()
	o, err := Discover(context.Background(), Config{Issuer: f.issuer, ClientID: testAudience, ClientSecret: "s3cret", ConsoleURL: "http://console.test", GroupPrefix: "stargate-"})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func cookie(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// login runs /api/auth/login and returns its redirect and the state cookie.
func (f *fakeIdP) login(t *testing.T, o *OIDC, next string) (*url.URL, *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	o.Login(w, httptest.NewRequest("GET", "/api/auth/login?next="+url.QueryEscape(next), nil))
	res := w.Result()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("login: %d", res.StatusCode)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	f.challenge, f.nonce = loc.Query().Get("code_challenge"), loc.Query().Get("nonce")
	return loc, cookie(res, loginCookie)
}

func (f *fakeIdP) callback(o *OIDC, state string, c *http.Cookie) *http.Response {
	req := httptest.NewRequest("GET", "/api/auth/callback?code=the-code&state="+url.QueryEscape(state), nil)
	if c != nil {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	o.Callback(w, req)
	return w.Result()
}

func TestLoginRedirectsWithPKCE(t *testing.T) {
	f := newFakeIdP(t)
	o := f.oidc(t)
	loc, c := f.login(t, o, "/keys?team=support")
	q := loc.Query()
	if !strings.HasPrefix(loc.String(), f.issuer+"/protocol/openid-connect/auth?") ||
		q.Get("client_id") != testAudience || q.Get("response_type") != "code" ||
		q.Get("redirect_uri") != "http://console.test/api/auth/callback" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("state") == "" || q.Get("nonce") == "" || !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("redirect %s", loc)
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("state cookie %+v", c)
	}
}

func TestCallbackSignsIn(t *testing.T) {
	f := newFakeIdP(t)
	o := f.oidc(t)
	loc, c := f.login(t, o, "/keys?team=support")
	res := f.callback(o, loc.Query().Get("state"), c)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "http://console.test/keys?team=support" {
		t.Fatalf("callback: %d → %s", res.StatusCode, res.Header.Get("Location"))
	}
	s := cookie(res, sessionCookie)
	if s == nil || !s.HttpOnly || s.SameSite != http.SameSiteLaxMode || s.Path != "/" {
		t.Fatalf("session cookie %+v", s)
	}
	if r := cookie(res, refreshCookie); r == nil || !r.HttpOnly {
		t.Fatalf("refresh cookie %+v", r)
	}
	// The session cookie signs in API requests.
	req := httptest.NewRequest("GET", "/api/v1/demo/session", nil)
	req.AddCookie(s)
	u, err := o.Authenticate(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "ada@example.com" || u.Name != "Ada Admin" || len(u.Roles) != 1 || u.Roles[0] != Admin {
		t.Fatalf("user %+v", u)
	}
}

func TestCallbackRefuses(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(*fakeIdP, *OIDC, *url.URL, *http.Cookie) *http.Response
	}{
		{"wrong state", func(f *fakeIdP, o *OIDC, _ *url.URL, c *http.Cookie) *http.Response {
			return f.callback(o, "forged", c)
		}},
		{"no state cookie", func(f *fakeIdP, o *OIDC, loc *url.URL, _ *http.Cookie) *http.Response {
			return f.callback(o, loc.Query().Get("state"), nil)
		}},
		{"wrong nonce", func(f *fakeIdP, o *OIDC, loc *url.URL, c *http.Cookie) *http.Response {
			f.tokenClaims = func(m map[string]any) { m["nonce"] = "replayed" }
			return f.callback(o, loc.Query().Get("state"), c)
		}},
		{"wrong audience", func(f *fakeIdP, o *OIDC, loc *url.URL, c *http.Cookie) *http.Response {
			f.tokenClaims = func(m map[string]any) { m["aud"] = "another-client" }
			return f.callback(o, loc.Query().Get("state"), c)
		}},
		{"wrong verifier", func(f *fakeIdP, o *OIDC, loc *url.URL, c *http.Cookie) *http.Response {
			f.challenge = "not-the-challenge"
			return f.callback(o, loc.Query().Get("state"), c)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeIdP(t)
			o := f.oidc(t)
			loc, ck := f.login(t, o, "/")
			res := c.run(f, o, loc, ck)
			if res.StatusCode == http.StatusFound || cookie(res, sessionCookie) != nil && cookie(res, sessionCookie).Value != "" {
				t.Fatalf("signed in: %d %v", res.StatusCode, res.Cookies())
			}
		})
	}
}

// next must stay on the console: anything else lands on its home page.
func TestLoginKeepsNextOnTheConsole(t *testing.T) {
	for next, want := range map[string]string{
		"/traffic?range=1h":    "http://console.test/traffic?range=1h",
		"//evil.example/":      "http://console.test/",
		"/\\evil.example":      "http://console.test/",
		"https://evil.example": "http://console.test/",
		"":                     "http://console.test/",
	} {
		f := newFakeIdP(t)
		o := f.oidc(t)
		loc, c := f.login(t, o, next)
		res := f.callback(o, loc.Query().Get("state"), c)
		if got := res.Header.Get("Location"); got != want {
			t.Errorf("next %q: went to %q, want %q", next, got, want)
		}
	}
}

func TestAuthenticate(t *testing.T) {
	f := newFakeIdP(t)
	o := f.oidc(t)
	now := time.Now()
	good := f.token(t, f.claims(now))
	expiredClaims := f.claims(now)
	expiredClaims["exp"] = now.Add(-time.Hour).Unix()
	expired := f.token(t, expiredClaims)

	req := func(method string, h map[string]string, cookies ...*http.Cookie) *http.Request {
		r := httptest.NewRequest(method, "http://console.test/api/v1/demo/budgets", nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		for _, c := range cookies {
			r.AddCookie(c)
		}
		return r
	}
	sess := func(v string) *http.Cookie { return &http.Cookie{Name: sessionCookie, Value: v} }

	t.Run("bearer", func(t *testing.T) {
		u, err := o.Authenticate(httptest.NewRecorder(), req("POST", map[string]string{"Authorization": "Bearer " + good}))
		if err != nil || u.Email != "ada@example.com" {
			t.Fatalf("%+v %v", u, err)
		}
	})
	t.Run("nothing", func(t *testing.T) {
		if _, err := o.Authenticate(httptest.NewRecorder(), req("GET", nil)); !errors.Is(err, ErrUnauthenticated) {
			t.Fatal(err)
		}
	})
	t.Run("bad bearer", func(t *testing.T) {
		if _, err := o.Authenticate(httptest.NewRecorder(), req("GET", map[string]string{"Authorization": "Bearer " + expired})); !errors.Is(err, ErrUnauthenticated) {
			t.Fatal(err)
		}
	})
	t.Run("expired cookie, no refresh", func(t *testing.T) {
		if _, err := o.Authenticate(httptest.NewRecorder(), req("GET", nil, sess(expired))); !errors.Is(err, ErrUnauthenticated) {
			t.Fatal(err)
		}
	})
	t.Run("expired cookie, refreshed", func(t *testing.T) {
		w := httptest.NewRecorder()
		u, err := o.Authenticate(w, req("GET", nil, sess(expired), &http.Cookie{Name: refreshCookie, Value: "the-refresh"}))
		if err != nil || u.Email != "ada@example.com" || f.refreshed != 1 {
			t.Fatalf("%+v %v (refreshed %d)", u, err, f.refreshed)
		}
		if c := cookie(w.Result(), sessionCookie); c == nil || c.Value == expired {
			t.Fatal("no new session cookie")
		}
	})
	t.Run("cross-site write", func(t *testing.T) {
		_, err := o.Authenticate(httptest.NewRecorder(), req("POST", map[string]string{"Origin": "https://evil.example"}, sess(good)))
		if !errors.Is(err, ErrCrossSite) {
			t.Fatal(err)
		}
	})
	t.Run("same-site write", func(t *testing.T) {
		if _, err := o.Authenticate(httptest.NewRecorder(), req("POST", map[string]string{"Origin": "http://console.test"}, sess(good))); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no email", func(t *testing.T) {
		c := f.claims(now)
		delete(c, "email")
		if _, err := o.Authenticate(httptest.NewRecorder(), req("GET", nil, sess(f.token(t, c)))); !errors.Is(err, ErrUnauthenticated) {
			t.Fatal(err)
		}
	})
}

func TestLogoutEndsTheKeycloakSessionToo(t *testing.T) {
	f := newFakeIdP(t)
	o := f.oidc(t)
	tok := f.token(t, f.claims(time.Now()))
	r := httptest.NewRequest("POST", "/api/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	w := httptest.NewRecorder()
	o.Logout(w, r)
	res := w.Result()
	var body struct {
		Redirect string `json:"redirect"`
	}
	json.NewDecoder(res.Body).Decode(&body)
	u, _ := url.Parse(body.Redirect)
	if res.StatusCode != 200 || !strings.HasPrefix(body.Redirect, f.issuer+"/protocol/openid-connect/logout?") ||
		u.Query().Get("id_token_hint") != tok || u.Query().Get("post_logout_redirect_uri") != "http://console.test/" {
		t.Fatalf("%d %q", res.StatusCode, body.Redirect)
	}
	for _, name := range []string{sessionCookie, refreshCookie} {
		if c := cookie(res, name); c == nil || c.MaxAge >= 0 {
			t.Errorf("%s not cleared: %+v", name, c)
		}
	}
}
