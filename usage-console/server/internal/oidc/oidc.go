// Package oidc verifies the access tokens an OpenID Connect provider
// (Keycloak) issues: RS256 signatures checked against the provider's
// published keys (JWKS), found through OIDC discovery.
package oidc

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Config is who the verifier trusts.
type Config struct {
	// Issuer is the provider's issuer URL, which tokens carry as iss.
	Issuer string
	// DiscoveryURL is where to read the provider's discovery document and
	// keys from, when that differs from Issuer (an in-cluster address, say).
	// Empty uses Issuer.
	DiscoveryURL string
	// ClientID is the client the tokens must be issued to.
	ClientID string
	// HTTPClient fetches discovery and keys; nil is a client with a 10 second timeout.
	HTTPClient *http.Client
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Claims are the parts of a token the console uses.
type Claims struct {
	Subject           string `json:"sub"`
	Name              string `json:"name"`
	GivenName         string `json:"given_name"`
	FamilyName        string `json:"family_name"`
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
}

// ErrInvalid is wrapped by every reason a token is refused.
var ErrInvalid = errors.New("invalid token")

// ErrUnavailable is wrapped when the provider's keys could not be read, so
// no token can be checked.
var ErrUnavailable = errors.New("identity provider unavailable")

// Verifier checks tokens. It is safe for concurrent use.
type Verifier struct {
	cfg     Config
	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func NewVerifier(cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, errors.New("oidc: an issuer and a client ID are required")
	}
	if cfg.DiscoveryURL == "" {
		cfg.DiscoveryURL = cfg.Issuer
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Verifier{cfg: cfg}, nil
}

// Verify checks raw, a compact JWT, and returns its claims.
func (v *Verifier) Verify(ctx context.Context, raw string) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("%w: not a JWT", ErrInvalid)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return Claims{}, fmt.Errorf("%w: header: %v", ErrInvalid, err)
	}
	if header.Alg != "RS256" {
		return Claims{}, fmt.Errorf("%w: alg %q, want RS256", ErrInvalid, header.Alg)
	}
	key, err := v.key(ctx, header.Kid)
	if err != nil {
		return Claims{}, err
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: signature encoding", ErrInvalid)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return Claims{}, fmt.Errorf("%w: bad signature", ErrInvalid)
	}
	var c struct {
		Claims
		Issuer    string   `json:"iss"`
		AuthParty string   `json:"azp"`
		Audience  audience `json:"aud"`
		Expiry    *int64   `json:"exp"`
		NotBefore *int64   `json:"nbf"`
	}
	if err := decodeSegment(parts[1], &c); err != nil {
		return Claims{}, fmt.Errorf("%w: claims: %v", ErrInvalid, err)
	}
	if c.Issuer != v.cfg.Issuer {
		return Claims{}, fmt.Errorf("%w: issuer %q, want %q", ErrInvalid, c.Issuer, v.cfg.Issuer)
	}
	if c.AuthParty != v.cfg.ClientID && !slices.Contains(c.Audience, v.cfg.ClientID) {
		return Claims{}, fmt.Errorf("%w: issued to %q, not %q", ErrInvalid, c.AuthParty, v.cfg.ClientID)
	}
	t := v.cfg.Now()
	if c.Expiry == nil || t.After(time.Unix(*c.Expiry, 0).Add(leeway)) {
		return Claims{}, fmt.Errorf("%w: expired", ErrInvalid)
	}
	if c.NotBefore != nil && t.Before(time.Unix(*c.NotBefore, 0).Add(-leeway)) {
		return Claims{}, fmt.Errorf("%w: not valid yet", ErrInvalid)
	}
	return c.Claims, nil
}

// leeway allows for clock skew between the provider and the console.
const leeway = 30 * time.Second

// audience is aud, which a JWT holds as one string or a list.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// b64 decodes a JWT's segments, refusing stray bits in the last character,
// so each token has one spelling.
var b64 = base64.RawURLEncoding.Strict()

func decodeSegment(s string, v any) error {
	b, err := b64.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// key returns the signing key kid names. The keys are read again when they
// are older than keysMaxAge, or when kid is unknown and they were last read
// over keysMinAge ago (Keycloak rotated its keys).
func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	age := v.cfg.Now().Sub(v.fetched)
	_, known := v.keys[kid]
	if v.keys == nil || age > keysMaxAge || (!known && age > keysMinAge) {
		keys, err := v.fetchKeys(ctx)
		if err != nil {
			return nil, err
		}
		v.keys, v.fetched = keys, v.cfg.Now()
	}
	k, ok := v.keys[kid]
	if !ok {
		return nil, fmt.Errorf("%w: unknown signing key %q", ErrInvalid, kid)
	}
	return k, nil
}

const (
	keysMaxAge = time.Hour
	keysMinAge = 30 * time.Second
)

// fetchKeys reads the provider's discovery document, then its RSA signing keys.
func (v *Verifier) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	var disc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := v.getJSON(ctx, strings.TrimSuffix(v.cfg.DiscoveryURL, "/")+"/.well-known/openid-configuration", &disc); err != nil {
		return nil, err
	}
	if disc.JWKSURI == "" {
		return nil, fmt.Errorf("%w: discovery has no jwks_uri", ErrUnavailable)
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := v.getJSON(ctx, disc.JWKSURI, &set); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) > 4 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	return keys, nil
}

func (v *Verifier) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	res, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: GET %s: %s", ErrUnavailable, url, res.Status)
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("%w: GET %s: %v", ErrUnavailable, url, err)
	}
	return nil
}
