package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The console signs in through the control plane (a backend-for-frontend),
// not in the browser: the control plane runs the authorization code flow
// with PKCE as a confidential client, validates the ID token, and keeps it
// in an HttpOnly, SameSite=Lax cookie that every /api request carries. The
// browser never holds a token script can read, and the SSE stream (an
// EventSource, which can't send an Authorization header) and file downloads
// are signed in like any fetch. Scripts and CI can send a Keycloak access
// token as a Bearer token instead.

var (
	// ErrUnauthenticated is a request with no valid session or token.
	ErrUnauthenticated = errors.New("not signed in")
	// ErrCrossSite is a cookie-authenticated write from another origin.
	ErrCrossSite = errors.New("cross-site request refused")
)

const (
	loginCookie   = "stargate_login"
	sessionCookie = "stargate_session"
	refreshCookie = "stargate_refresh"
	// CallbackPath is where Keycloak sends the browser back, under the
	// console's origin (Vite proxies /api to the control plane).
	CallbackPath = "/api/auth/callback"
)

// Config is how the control plane signs people in.
type Config struct {
	// Issuer is the realm's URL (http://localhost:8180/realms/nebari).
	Issuer       string
	ClientID     string
	ClientSecret string
	// ConsoleURL is the console's origin as the browser sees it
	// (http://localhost:5173). The callback is ConsoleURL+CallbackPath, and
	// sign-out returns to ConsoleURL.
	ConsoleURL string
	// GroupPrefix marks the Keycloak groups that are Stargate roles
	// ("stargate-" makes /stargate-admin the admin role).
	GroupPrefix string
}

// User is who a request acts as, and their roles.
type User struct {
	Email string
	Name  string
	Roles []Role
}

// OIDC signs people in against one issuer.
type OIDC struct {
	Config
	Verifier      *Verifier
	AuthURL       string
	TokenURL      string
	EndSessionURL string
	HTTP          *http.Client
}

// Discover reads the issuer's OpenID configuration.
func Discover(ctx context.Context, c Config) (*OIDC, error) {
	if c.Issuer == "" || c.ClientID == "" || c.ConsoleURL == "" {
		return nil, errors.New("OIDC needs an issuer, a client id and the console's URL")
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery: %s", res.Status)
	}
	var d struct {
		Issuer        string `json:"issuer"`
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
		EndSession    string `json:"end_session_endpoint"`
		JWKS          string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(res.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}
	if d.Issuer != c.Issuer {
		return nil, fmt.Errorf("OIDC discovery: issuer is %q, not %q", d.Issuer, c.Issuer)
	}
	c.ConsoleURL = strings.TrimSuffix(c.ConsoleURL, "/")
	return &OIDC{
		Config:        c,
		Verifier:      &Verifier{Issuer: c.Issuer, Audience: c.ClientID, JWKSURL: d.JWKS, Client: hc},
		AuthURL:       d.Authorization,
		TokenURL:      d.Token,
		EndSessionURL: d.EndSession,
		HTTP:          hc,
	}, nil
}

func (o *OIDC) secure() bool { return strings.HasPrefix(o.ConsoleURL, "https://") }

func (o *OIDC) setCookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge, HttpOnly: true, Secure: o.secure(), SameSite: http.SameSiteLaxMode})
}

func randomString() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// loginState is what the login cookie carries to the callback.
type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
}

// safeNext keeps a post-login destination on the console: a path, never
// another origin ("//evil", "/\evil" and absolute URLs become "/").
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return next
}

// Login is GET /api/auth/login?next=/path: it sends the browser to Keycloak.
func (o *OIDC) Login(w http.ResponseWriter, r *http.Request) {
	st := loginState{State: randomString(), Nonce: randomString(), Verifier: randomString(), Next: safeNext(r.URL.Query().Get("next"))}
	b, _ := json.Marshal(st)
	o.setCookie(w, loginCookie, base64.RawURLEncoding.EncodeToString(b), "/api/auth/", 600)
	sum := sha256.Sum256([]byte(st.Verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {o.ClientID},
		"redirect_uri":          {o.ConsoleURL + CallbackPath},
		"scope":                 {"openid email profile"},
		"state":                 {st.State},
		"nonce":                 {st.Nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, o.AuthURL+"?"+q.Encode(), http.StatusFound)
}

func authError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "Sign-in failed: %s\n\nStart again at /api/auth/login.\n", msg)
}

// Callback is GET /api/auth/callback: Keycloak's answer. It checks the state,
// exchanges the code (with the PKCE verifier), validates the ID token and its
// nonce, and sets the session cookie.
func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(loginCookie)
	o.setCookie(w, loginCookie, "", "/api/auth/", -1)
	if err != nil {
		authError(w, http.StatusBadRequest, "the sign-in took too long or started elsewhere")
		return
	}
	var st loginState
	if b, err := base64.RawURLEncoding.DecodeString(c.Value); err != nil || json.Unmarshal(b, &st) != nil {
		authError(w, http.StatusBadRequest, "unreadable sign-in state")
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		authError(w, http.StatusUnauthorized, e+": "+q.Get("error_description"))
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		authError(w, http.StatusBadRequest, "state mismatch")
		return
	}
	tok, err := o.token(r.Context(), url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {q.Get("code")},
		"redirect_uri":  {o.ConsoleURL + CallbackPath},
		"code_verifier": {st.Verifier},
	})
	if err != nil {
		log.Printf("oidc callback: %v", err)
		authError(w, http.StatusBadGateway, "Keycloak didn't issue a token")
		return
	}
	claims, err := o.Verifier.Verify(r.Context(), tok.IDToken)
	if err == nil && subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(st.Nonce)) != 1 {
		err = errors.New("nonce mismatch")
	}
	if err == nil && claims.Email == "" {
		err = errors.New("the token has no email")
	}
	if err != nil {
		log.Printf("oidc callback: %v", err)
		authError(w, http.StatusUnauthorized, "the ID token didn't validate")
		return
	}
	o.setSession(w, tok)
	http.Redirect(w, r, o.ConsoleURL+st.Next, http.StatusFound)
}

type tokenResponse struct {
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
}

func (o *OIDC) token(ctx context.Context, form url.Values) (tokenResponse, error) {
	if o.ClientSecret == "" {
		form.Set("client_id", o.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if o.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(o.ClientID), url.QueryEscape(o.ClientSecret))
	}
	res, err := o.HTTP.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return tokenResponse{}, fmt.Errorf("token endpoint: %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	var t tokenResponse
	if err := json.NewDecoder(res.Body).Decode(&t); err != nil {
		return tokenResponse{}, err
	}
	if t.IDToken == "" {
		return tokenResponse{}, errors.New("token endpoint: no id_token (is the openid scope allowed?)")
	}
	return t, nil
}

// setSession stores the tokens as browser-session cookies: the ID token is
// validated on every request (its exp ends it), and the refresh token
// renews it until Keycloak's SSO session ends.
func (o *OIDC) setSession(w http.ResponseWriter, t tokenResponse) {
	o.setCookie(w, sessionCookie, t.IDToken, "/", 0)
	if t.RefreshToken != "" {
		o.setCookie(w, refreshCookie, t.RefreshToken, "/", 0)
	}
}

func (o *OIDC) clearSession(w http.ResponseWriter) {
	o.setCookie(w, sessionCookie, "", "/", -1)
	o.setCookie(w, refreshCookie, "", "/", -1)
}

// Logout is POST /api/auth/logout. It clears the cookies and answers
// {"redirect": url}: Keycloak's end-session endpoint, which ends the SSO
// session too and comes back to the console.
func (o *OIDC) Logout(w http.ResponseWriter, r *http.Request) {
	redirect := o.ConsoleURL + "/"
	if o.EndSessionURL != "" {
		q := url.Values{"post_logout_redirect_uri": {redirect}, "client_id": {o.ClientID}}
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			q.Set("id_token_hint", c.Value)
		}
		redirect = o.EndSessionURL + "?" + q.Encode()
	}
	o.clearSession(w)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"redirect": redirect})
}

func (o *OIDC) user(c Claims) (User, error) {
	if c.Email == "" {
		return User{}, fmt.Errorf("%w: the token has no email", ErrUnauthenticated)
	}
	return User{Email: c.Email, Name: c.Name, Roles: RolesFromGroups(c.Groups, o.GroupPrefix)}, nil
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// Authenticate is who r acts as: a Bearer token, or the session cookie
// (renewed with the refresh cookie once expired, writing new cookies to w).
// A cookie-authenticated write must come from the console's origin (SameSite
// already keeps other sites' cookies off it; this also catches same-site
// origins on other ports).
func (o *OIDC) Authenticate(w http.ResponseWriter, r *http.Request) (User, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		raw, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			return User{}, fmt.Errorf("%w: only Bearer tokens are accepted", ErrUnauthenticated)
		}
		c, err := o.Verifier.Verify(r.Context(), strings.TrimSpace(raw))
		if err != nil {
			return User{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
		}
		return o.user(c)
	}
	sc, err := r.Cookie(sessionCookie)
	if err != nil || sc.Value == "" {
		return User{}, ErrUnauthenticated
	}
	if !safeMethod(r.Method) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != o.ConsoleURL {
			return User{}, ErrCrossSite
		}
	}
	c, err := o.Verifier.Verify(r.Context(), sc.Value)
	if errors.Is(err, ErrExpired) {
		if rc, rerr := r.Cookie(refreshCookie); rerr == nil && rc.Value != "" {
			c, err = o.refresh(w, r, rc.Value)
		}
	}
	if err != nil {
		return User{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	return o.user(c)
}

func (o *OIDC) refresh(w http.ResponseWriter, r *http.Request, refreshToken string) (Claims, error) {
	tok, err := o.token(r.Context(), url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}})
	if err != nil {
		o.clearSession(w)
		return Claims{}, err
	}
	c, err := o.Verifier.Verify(r.Context(), tok.IDToken)
	if err != nil {
		o.clearSession(w)
		return Claims{}, err
	}
	o.setSession(w, tok)
	return c, nil
}
