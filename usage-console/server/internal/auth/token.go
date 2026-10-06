// Package auth signs people into the console with OIDC (Keycloak, realm
// nebari) and turns their token's groups into Stargate roles (spec §5.2,
// backend-decisions §7). It validates tokens itself, with the standard
// library: an ID token or access token is a JWT signed by a key in the
// realm's JWKS, and it must name our issuer and our client, and not be
// expired.
package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	// ErrInvalid is a token that isn't ours or isn't intact: malformed, an
	// algorithm we don't take, a bad signature, or the wrong issuer, audience
	// or nonce.
	ErrInvalid = errors.New("invalid token")
	// ErrExpired is a token that was valid and has expired: a refresh, or
	// signing in again, fixes it.
	ErrExpired = errors.New("token expired")
)

// Claims is what Stargate reads from a token.
type Claims struct {
	Subject string
	Email   string
	Name    string
	Groups  []string
	Nonce   string
	Expiry  time.Time
}

// Verifier checks tokens from one issuer for one audience (our client id).
type Verifier struct {
	Issuer   string
	Audience string
	// JWKSURL is the issuer's jwks_uri (from discovery).
	JWKSURL string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Leeway is the clock skew allowed on exp and nbf; 0 is 30s.
	Leeway time.Duration
	// Client fetches the key set; nil is a client with a 10s timeout.
	Client *http.Client

	mu      sync.Mutex
	keys    map[string]crypto.PublicKey
	fetched time.Time
}

// refetchEvery bounds how often an unknown kid refetches the key set, so a
// stream of forged kids can't make us hammer the IdP.
const refetchEvery = 30 * time.Second

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type payload struct {
	Iss               string   `json:"iss"`
	Sub               string   `json:"sub"`
	Aud               audience `json:"aud"`
	Exp               *int64   `json:"exp"`
	Nbf               *int64   `json:"nbf"`
	Email             string   `json:"email"`
	PreferredUsername string   `json:"preferred_username"`
	Name              string   `json:"name"`
	Groups            []string `json:"groups"`
	Nonce             string   `json:"nonce"`
}

// audience is aud as a string or a list of them (both are allowed, RFC 7519).
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
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

// Verify checks raw's signature against the issuer's keys, then its issuer,
// audience, expiry and not-before. An expired token is ErrExpired, anything
// else wrong ErrInvalid (both wrapped with the reason).
func (v *Verifier) Verify(ctx context.Context, raw string) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("%w: not a JWT", ErrInvalid)
	}
	var h header
	if err := decodePart(parts[0], &h); err != nil {
		return Claims{}, fmt.Errorf("%w: header: %v", ErrInvalid, err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: signature encoding", ErrInvalid)
	}
	key, err := v.key(ctx, h.Kid)
	if err != nil {
		return Claims{}, err
	}
	if err := checkSignature(h.Alg, key, parts[0]+"."+parts[1], sig); err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var p payload
	if err := decodePart(parts[1], &p); err != nil {
		return Claims{}, fmt.Errorf("%w: payload: %v", ErrInvalid, err)
	}
	leeway := v.Leeway
	if leeway == 0 {
		leeway = 30 * time.Second
	}
	now := v.now()
	switch {
	case p.Iss != v.Issuer:
		return Claims{}, fmt.Errorf("%w: issuer %q", ErrInvalid, p.Iss)
	case !contains(p.Aud, v.Audience):
		return Claims{}, fmt.Errorf("%w: audience %v", ErrInvalid, []string(p.Aud))
	case p.Exp == nil:
		return Claims{}, fmt.Errorf("%w: no expiry", ErrInvalid)
	case p.Nbf != nil && now.Add(leeway).Before(time.Unix(*p.Nbf, 0)):
		return Claims{}, fmt.Errorf("%w: not valid yet", ErrInvalid)
	case !now.Add(-leeway).Before(time.Unix(*p.Exp, 0)):
		return Claims{}, fmt.Errorf("%w at %s", ErrExpired, time.Unix(*p.Exp, 0).UTC().Format(time.RFC3339))
	}
	email := p.Email
	if email == "" {
		email = p.PreferredUsername
	}
	return Claims{Subject: p.Sub, Email: strings.ToLower(email), Name: p.Name, Groups: p.Groups, Nonce: p.Nonce, Expiry: time.Unix(*p.Exp, 0)}, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func decodePart(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// checkSignature takes only the asymmetric algorithms an OIDC provider signs
// with; "none" and HMAC (which would let a public key be used as a secret)
// are refused.
func checkSignature(alg string, key crypto.PublicKey, signed string, sig []byte) error {
	sum := sha256.Sum256([]byte(signed))
	switch alg {
	case "RS256":
		k, ok := key.(*rsa.PublicKey)
		if !ok {
			return errors.New("RS256 with a non-RSA key")
		}
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, sum[:], sig)
	case "PS256":
		k, ok := key.(*rsa.PublicKey)
		if !ok {
			return errors.New("PS256 with a non-RSA key")
		}
		return rsa.VerifyPSS(k, crypto.SHA256, sum[:], sig, nil)
	case "ES256":
		k, ok := key.(*ecdsa.PublicKey)
		if !ok || len(sig) != 64 {
			return errors.New("ES256 with a non-EC key or a malformed signature")
		}
		r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(k, sum[:], r, s) {
			return errors.New("bad signature")
		}
		return nil
	default:
		return fmt.Errorf("algorithm %q not accepted", alg)
	}
}

// key is the issuer's signing key kid. An unknown kid refetches the set (the
// IdP may have rotated), at most once per refetchEvery.
func (v *Verifier) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	if v.keys != nil && v.now().Sub(v.fetched) < refetchEvery {
		return nil, fmt.Errorf("%w: unknown key %q", ErrInvalid, kid)
	}
	keys, err := v.fetch(ctx)
	v.fetched = v.now()
	if err != nil {
		// Keep the old set; the next unknown kid tries again later.
		return nil, fmt.Errorf("fetch signing keys: %w", err)
	}
	v.keys = keys
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: unknown key %q", ErrInvalid, kid)
}

type jsonWebKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (v *Verifier) fetch(ctx context.Context) (map[string]crypto.PublicKey, error) {
	c := v.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", v.JWKSURL, res.Status)
	}
	var set struct {
		Keys []jsonWebKey `json:"keys"`
	}
	if err := json.NewDecoder(res.Body).Decode(&set); err != nil {
		return nil, err
	}
	out := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue // Keycloak also publishes its encryption key
		}
		switch k.Kty {
		case "RSA":
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil || len(e) > 4 {
				continue
			}
			out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		case "EC":
			if k.Crv != "P-256" {
				continue
			}
			x, err1 := base64.RawURLEncoding.DecodeString(k.X)
			y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
			if err1 != nil || err2 != nil {
				continue
			}
			out[k.Kid] = &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		}
	}
	return out, nil
}
