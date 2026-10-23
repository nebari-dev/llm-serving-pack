package oidc_test

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
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/oidc"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

const client = "usage-openmeter-console"

// idp is a stand-in identity provider: OIDC discovery, a JWKS that can be
// rotated, and tokens signed with its keys.
type idp struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	keys    map[string]*rsa.PrivateKey // kid -> key, as the JWKS publishes them
	fetches int
}

func newIdP(t *testing.T) *idp {
	p := &idp{t: t, keys: map[string]*rsa.PrivateKey{}}
	p.addKey("k1")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /realms/nebari/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": p.issuer(), "jwks_uri": p.srv.URL + "/realms/nebari/protocol/openid-connect/certs"})
	})
	mux.HandleFunc("GET /realms/nebari/protocol/openid-connect/certs", func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.fetches++
		var keys []map[string]string
		for kid, k := range p.keys {
			keys = append(keys, map[string]string{
				"kid": kid, "kty": "RSA", "alg": "RS256", "use": "sig",
				"n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes()),
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *idp) issuer() string { return p.srv.URL + "/realms/nebari" }

func (p *idp) addKey(kid string) *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	p.keys[kid] = k
	p.mu.Unlock()
	return k
}

func (p *idp) key(kid string) *rsa.PrivateKey {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.keys[kid]
}

// claims are what Keycloak puts in an access token for the console's client.
func (p *idp) claims() map[string]any {
	return map[string]any{
		"iss": p.issuer(), "sub": "u-1", "typ": "Bearer", "azp": client, "aud": "account",
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(4 * time.Minute).Unix(),
		"name": "Alice Example", "given_name": "Alice", "family_name": "Example",
		"preferred_username": "alice", "email": "alice@example.com",
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func sign(t *testing.T, kid string, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	c, _ := json.Marshal(claims)
	input := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + b64(sig)
}

func (p *idp) verifier() *oidc.Verifier { return p.verifierAt(func() time.Time { return now }) }

func (p *idp) verifierAt(clock func() time.Time) *oidc.Verifier {
	v, err := oidc.NewVerifier(oidc.Config{Issuer: p.issuer(), ClientID: client, Now: clock})
	if err != nil {
		p.t.Fatal(err)
	}
	return v
}

func TestVerifyAcceptsAKeycloakAccessToken(t *testing.T) {
	p := newIdP(t)

	c, err := p.verifier().Verify(context.Background(), sign(t, "k1", p.key("k1"), p.claims()))

	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Alice Example" || c.Email != "alice@example.com" || c.PreferredUsername != "alice" || c.Subject != "u-1" {
		t.Fatalf("claims %+v", c)
	}
}

// Tokens the console must refuse: each differs from a good one in one way.
func TestVerifyRefuses(t *testing.T) {
	p := newIdP(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	with := func(k string, v any) map[string]any { c := p.claims(); c[k] = v; return c }
	without := func(k string) map[string]any { c := p.claims(); delete(c, k); return c }
	good := sign(t, "k1", p.key("k1"), p.claims())
	parts := strings.Split(good, ".")
	cases := map[string]string{
		"expired":                      sign(t, "k1", p.key("k1"), with("exp", now.Add(-time.Minute).Unix())),
		"no expiry":                    sign(t, "k1", p.key("k1"), without("exp")),
		"not yet valid":                sign(t, "k1", p.key("k1"), with("nbf", now.Add(time.Minute).Unix())),
		"another issuer":               sign(t, "k1", p.key("k1"), with("iss", "https://evil.example.com/realms/nebari")),
		"another client":               sign(t, "k1", p.key("k1"), with("azp", "some-other-app")),
		"signed with another key":      sign(t, "k1", other, p.claims()),
		"claims swapped after signing": parts[0] + "." + b64([]byte(`{"iss":"`+p.issuer()+`","azp":"`+client+`","exp":9999999999,"email":"mallory@example.com"}`)) + "." + parts[2],
		"unsigned (alg none)":          b64([]byte(`{"alg":"none","kid":"k1"}`)) + "." + parts[1] + ".",
		// The signature's last character carries 4 unused bits; a token with
		// them set is not the token Keycloak issued.
		"stray bits in the signature": good[:len(good)-1] + string(flipLow(good[len(good)-1])),
		"not a JWT":                   "opaque-token",
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if c, err := p.verifier().Verify(context.Background(), tok); !errors.Is(err, oidc.ErrInvalid) {
				t.Fatalf("got %+v, %v; want ErrInvalid", c, err)
			}
		})
	}
}

// Within a small leeway, clock skew between Keycloak and the console does
// not refuse a token.
func TestVerifyAllowsSmallClockSkew(t *testing.T) {
	p := newIdP(t)
	c := p.claims()
	c["exp"] = now.Add(-10 * time.Second).Unix()
	c["nbf"] = now.Add(10 * time.Second).Unix()

	if _, err := p.verifier().Verify(context.Background(), sign(t, "k1", p.key("k1"), c)); err != nil {
		t.Fatal(err)
	}
}

// A token issued to the console's client is accepted whether the client is
// named in azp (Keycloak access tokens) or in aud.
func TestVerifyAcceptsTheClientInAud(t *testing.T) {
	p := newIdP(t)
	c := p.claims()
	c["azp"] = "nebari-cli"
	c["aud"] = []string{"account", client}

	if _, err := p.verifier().Verify(context.Background(), sign(t, "k1", p.key("k1"), c)); err != nil {
		t.Fatal(err)
	}
}

func (p *idp) fetchCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fetches
}

// When Keycloak rotates its keys, a token signed with a key the console has
// not seen makes it read the keys again.
func TestVerifyRereadsKeysForAnUnknownKid(t *testing.T) {
	p := newIdP(t)
	clock := now
	v := p.verifierAt(func() time.Time { return clock })
	if _, err := v.Verify(context.Background(), sign(t, "k1", p.key("k1"), p.claims())); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Minute)
	k2 := p.addKey("k2")
	if _, err := v.Verify(context.Background(), sign(t, "k2", k2, p.claims())); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	if p.fetchCount() != 2 {
		t.Fatalf("keys read %d times, want 2", p.fetchCount())
	}
}

// Tokens naming unknown keys, one after another, read the keys at most once
// in a short while, so nobody can make the console flood Keycloak.
func TestVerifyDoesNotRereadKeysOnEveryUnknownKid(t *testing.T) {
	p := newIdP(t)
	v := p.verifier()
	for i := range 5 {
		if _, err := v.Verify(context.Background(), sign(t, "nope", p.key("k1"), p.claims())); !errors.Is(err, oidc.ErrInvalid) {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if p.fetchCount() != 1 {
		t.Fatalf("keys read %d times, want 1", p.fetchCount())
	}
}

// Keys are read again after a while even when every kid is known, so a key
// Keycloak withdrew stops being trusted.
func TestVerifyRefreshesKeysPeriodically(t *testing.T) {
	p := newIdP(t)
	clock := now
	v := p.verifierAt(func() time.Time { return clock })
	tok := sign(t, "k1", p.key("k1"), p.claims())
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatal(err)
	}

	k1 := p.key("k1")
	p.mu.Lock()
	delete(p.keys, "k1")
	p.mu.Unlock()
	clock = clock.Add(2 * time.Hour)
	c := p.claims()
	c["exp"] = clock.Add(time.Minute).Unix()
	if _, err := v.Verify(context.Background(), sign(t, "k1", k1, c)); !errors.Is(err, oidc.ErrInvalid) {
		t.Fatal("a withdrawn key is still trusted")
	}
}

// Without an issuer and a client there is nothing to check tokens against.
func TestNewVerifierNeedsIssuerAndClient(t *testing.T) {
	for _, cfg := range []oidc.Config{{Issuer: "https://id.example.com/realms/nebari"}, {ClientID: client}} {
		if _, err := oidc.NewVerifier(cfg); err == nil {
			t.Fatalf("%+v: no error", cfg)
		}
	}
}

// Tokens carry Keycloak's public issuer URL while the console may read the
// keys from an in-cluster address.
func TestVerifyReadsKeysFromTheDiscoveryURL(t *testing.T) {
	p := newIdP(t)
	public := "https://id.example.com/realms/nebari"
	v, err := oidc.NewVerifier(oidc.Config{Issuer: public, DiscoveryURL: p.issuer(), ClientID: client, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	c := p.claims()
	c["iss"] = public

	if _, err := v.Verify(context.Background(), sign(t, "k1", p.key("k1"), c)); err != nil {
		t.Fatal(err)
	}
}

// flipLow sets the lowest bit of a base64url character's value.
func flipLow(c byte) byte {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	return alphabet[strings.IndexByte(alphabet, c)^1]
}
