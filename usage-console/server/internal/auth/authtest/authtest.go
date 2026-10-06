// Package authtest is an OIDC issuer for tests: a JWKS endpoint and tokens
// signed with its key, and an auth.OIDC that trusts it.
package authtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/auth"
)

const (
	Issuer   = "http://idp.test/realms/nebari"
	ClientID = "stargate-console"
	Console  = "http://console.test"
)

type IdP struct {
	key *rsa.PrivateKey
	srv *httptest.Server
}

func New(t testing.TB) *IdP {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &IdP{key: k}
	idp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(k.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.PublicKey.E)).Bytes()),
		}}})
	}))
	t.Cleanup(idp.srv.Close)
	return idp
}

// OIDC trusts this issuer, with groups prefixed "stargate-" as roles.
func (idp *IdP) OIDC() *auth.OIDC {
	return &auth.OIDC{
		Config:   auth.Config{Issuer: Issuer, ClientID: ClientID, ConsoleURL: Console, GroupPrefix: "stargate-"},
		Verifier: &auth.Verifier{Issuer: Issuer, Audience: ClientID, JWKSURL: idp.srv.URL},
		HTTP:     http.DefaultClient,
	}
}

// Token is a valid token for email, in the given Keycloak groups.
func (idp *IdP) Token(t testing.TB, email, name string, groups ...string) string {
	t.Helper()
	now := time.Now()
	h, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test"})
	p, _ := json.Marshal(map[string]any{
		"iss": Issuer, "aud": ClientID, "sub": email, "email": email, "name": name, "groups": groups,
		"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
	})
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}
