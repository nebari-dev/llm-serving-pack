package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testIdP is a JWKS endpoint serving one RSA key, and a way to mint tokens
// with it (or with a key it doesn't publish).
type testIdP struct {
	key   *rsa.PrivateKey
	kid   string
	srv   *httptest.Server
	hits  atomic.Int32
	other *rsa.PrivateKey
}

const (
	testIssuer   = "http://idp.test/realms/nebari"
	testAudience = "stargate-console"
)

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()
	idp := &testIdP{key: mustKey(t), other: mustKey(t), kid: "k1"}
	idp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		idp.hits.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{jwk(idp.kid, &idp.key.PublicKey)}})
	}))
	t.Cleanup(idp.srv.Close)
	return idp
}

func mustKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func jwk(kid string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func (idp *testIdP) verifier(now time.Time) *Verifier {
	return &Verifier{Issuer: testIssuer, Audience: testAudience, JWKSURL: idp.srv.URL, Now: func() time.Time { return now }}
}

// claims is a valid token's claims at now; tests change one thing.
func claims(now time.Time) map[string]any {
	return map[string]any{
		"iss": testIssuer, "aud": testAudience, "sub": "u-1",
		"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Add(-time.Minute).Unix(),
		"email": "ada@example.com", "name": "Ada Admin",
		"groups": []string{"/stargate-admin", "/other-app"},
	}
}

func sign(t *testing.T, key *rsa.PrivateKey, header, payload map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(payload)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (idp *testIdP) token(t *testing.T, c map[string]any) string {
	return sign(t, idp.key, map[string]any{"alg": "RS256", "typ": "JWT", "kid": idp.kid}, c)
}

func TestVerifyAcceptsAGoodToken(t *testing.T) {
	idp := newTestIdP(t)
	now := time.Now()
	got, err := idp.verifier(now).Verify(context.Background(), idp.token(t, claims(now)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "ada@example.com" || got.Name != "Ada Admin" || got.Subject != "u-1" {
		t.Fatalf("claims %+v", got)
	}
	if len(got.Groups) != 2 || got.Groups[0] != "/stargate-admin" {
		t.Fatalf("groups %v", got.Groups)
	}
}

// Keycloak's access tokens carry aud as a list (["stargate-console", "account"]).
func TestVerifyAcceptsAnAudienceList(t *testing.T) {
	idp := newTestIdP(t)
	now := time.Now()
	c := claims(now)
	c["aud"] = []string{"account", testAudience}
	if _, err := idp.verifier(now).Verify(context.Background(), idp.token(t, c)); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRefuses(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name string
		tok  func(*testing.T, *testIdP) string
		want error
	}{
		{"expired", func(t *testing.T, idp *testIdP) string {
			c := claims(now)
			c["exp"] = now.Add(-2 * time.Minute).Unix()
			return idp.token(t, c)
		}, ErrExpired},
		{"no expiry", func(t *testing.T, idp *testIdP) string {
			c := claims(now)
			delete(c, "exp")
			return idp.token(t, c)
		}, ErrInvalid},
		{"not yet valid", func(t *testing.T, idp *testIdP) string {
			c := claims(now)
			c["nbf"] = now.Add(10 * time.Minute).Unix()
			return idp.token(t, c)
		}, ErrInvalid},
		{"wrong issuer", func(t *testing.T, idp *testIdP) string {
			c := claims(now)
			c["iss"] = "http://idp.test/realms/master"
			return idp.token(t, c)
		}, ErrInvalid},
		{"wrong audience", func(t *testing.T, idp *testIdP) string {
			c := claims(now)
			c["aud"] = "some-other-client"
			return idp.token(t, c)
		}, ErrInvalid},
		{"wrong audience list", func(t *testing.T, idp *testIdP) string {
			c := claims(now)
			c["aud"] = []string{"account"}
			return idp.token(t, c)
		}, ErrInvalid},
		{"bad signature", func(t *testing.T, idp *testIdP) string {
			// Signed by a key the IdP doesn't publish, under the published kid.
			return sign(t, idp.other, map[string]any{"alg": "RS256", "kid": idp.kid}, claims(now))
		}, ErrInvalid},
		{"tampered payload", func(t *testing.T, idp *testIdP) string {
			parts := strings.Split(idp.token(t, claims(now)), ".")
			c := claims(now)
			c["groups"] = []string{"/stargate-owner"}
			p, _ := json.Marshal(c)
			return parts[0] + "." + base64.RawURLEncoding.EncodeToString(p) + "." + parts[2]
		}, ErrInvalid},
		{"alg none", func(t *testing.T, idp *testIdP) string {
			h, _ := json.Marshal(map[string]any{"alg": "none", "kid": idp.kid})
			p, _ := json.Marshal(claims(now))
			return base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p) + "."
		}, ErrInvalid},
		{"HS256 with the public key", func(t *testing.T, idp *testIdP) string {
			return sign(t, idp.key, map[string]any{"alg": "HS256", "kid": idp.kid}, claims(now))
		}, ErrInvalid},
		{"unknown kid", func(t *testing.T, idp *testIdP) string {
			return sign(t, idp.key, map[string]any{"alg": "RS256", "kid": "nope"}, claims(now))
		}, ErrInvalid},
		{"not a JWT", func(*testing.T, *testIdP) string { return "abc.def" }, ErrInvalid},
	} {
		t.Run(c.name, func(t *testing.T) {
			idp := newTestIdP(t)
			_, err := idp.verifier(now).Verify(context.Background(), c.tok(t, idp))
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

// A kid the cached set doesn't have refetches the set once (the IdP rotated
// its key), but a flood of unknown kids doesn't hammer the IdP.
func TestVerifyRefetchesTheKeySetForANewKid(t *testing.T) {
	idp := newTestIdP(t)
	now := time.Now()
	v := idp.verifier(now)
	v.Now = func() time.Time { return now }
	if _, err := v.Verify(context.Background(), idp.token(t, claims(now))); err != nil {
		t.Fatal(err)
	}
	idp.key, idp.kid = mustKey(t), "k2"
	now = now.Add(refetchEvery + time.Second)
	if _, err := v.Verify(context.Background(), idp.token(t, claims(now))); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	before := idp.hits.Load()
	for range 5 {
		v.Verify(context.Background(), sign(t, idp.key, map[string]any{"alg": "RS256", "kid": "bogus"}, claims(now)))
	}
	if n := idp.hits.Load() - before; n > 1 {
		t.Fatalf("%d refetches for unknown kids", n)
	}
}

func TestRolesFromGroups(t *testing.T) {
	for _, c := range []struct {
		groups []string
		want   []Role
	}{
		{nil, nil},
		{[]string{"/stargate-admin"}, []Role{Admin}},
		{[]string{"stargate-finance", "/stargate-security"}, []Role{Security, Finance}},
		{[]string{"/nebari/stargate-editor"}, []Role{Editor}},
		{[]string{"/admin", "/developer", "/stargate-nope"}, nil},
		{[]string{"/stargate-viewer", "/stargate-owner", "/stargate-viewer"}, []Role{Owner, Viewer}},
	} {
		got := RolesFromGroups(c.groups, "stargate-")
		if !equalRoles(got, c.want) {
			t.Errorf("%v: got %v, want %v", c.groups, got, c.want)
		}
	}
}

func equalRoles(a, b []Role) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
