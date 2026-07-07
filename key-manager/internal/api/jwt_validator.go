package api

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// retryMaxAttempts and retryInitialBackoff control the *active* JWKS fetch
// retry loop run on the background init goroutine. After this budget is
// exhausted the goroutine switches to slowPollInterval to keep trying
// indefinitely without the exponential blow-up. They are package-level
// variables so tests can override them without incurring real sleep time.
var (
	retryMaxAttempts    = 5
	retryInitialBackoff = 2 * time.Second
	// retryDelay is called between attempts; replaced in tests to avoid sleeping.
	retryDelay = time.Sleep
	// slowPollInterval is the cadence for the post-retry "keep trying" loop.
	slowPollInterval = 30 * time.Second
)

// ErrNotReady is returned by ValidateToken when the validator's initial JWKS
// fetch has not completed yet. Callers should treat this as transient and
// surface a 503 Service Unavailable so clients distinguish "Keycloak not
// reachable yet" from "your token is bad" (401).
var ErrNotReady = errors.New("jwt validator: initial JWKS fetch not yet complete")

// clockLeeway is the tolerance applied when validating the "exp" claim. A few
// seconds may elapse between the SPA minting the token and the request reaching
// the key-manager through nginx; without leeway, small clock drift causes
// spurious "token expired" errors. 30s is generous but still provides real
// expiry protection (Keycloak's default access-token lifetime is 5 minutes).
const clockLeeway = 30 * time.Second

// jwk is a single JSON Web Key from Keycloak's JWKS endpoint.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// jwks is the JSON Web Key Set returned by Keycloak.
type jwks struct {
	Keys []jwk `json:"keys"`
}

// JWTValidator validates bearer tokens minted by Keycloak against the realm's
// JWKS. Under Model B (SPA-managed Keycloak) there is no gateway JWT
// enforcement, so the key-manager verifies the token itself: RSA signature,
// expiry (with clockLeeway), and issuer.
//
// The initial JWKS fetch runs asynchronously in a background goroutine started
// by NewJWTValidator so process startup does not block on Keycloak being
// reachable (avoids CrashLoopBackOff when Keycloak is slow to come up). While
// ready is false, ValidateToken returns ErrNotReady, which the auth middleware
// surfaces as 503 Service Unavailable — distinct from a bad token (401).
type JWTValidator struct {
	logger      *slog.Logger
	keycloakURL string
	// issuerURL validates the `iss` claim. It defaults to keycloakURL but is
	// overridden with SetIssuerURL when the external Keycloak URL (embedded in
	// tokens as `iss`) differs from the internal cluster URL used for JWKS
	// fetching.
	issuerURL  string
	realm      string
	publicKeys map[string]*rsa.PublicKey
	keysMu     sync.RWMutex
	lastFetch  time.Time
	// ready flips to true once the first JWKS fetch succeeds. It is atomic
	// because the writer runs on the background init goroutine while readers
	// run on every request-handling goroutine.
	ready    atomic.Bool
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

// NewJWTValidator creates a validator and returns immediately; the initial
// JWKS fetch runs on a background goroutine. It first runs retryMaxAttempts
// active attempts with exponential backoff, then falls back to a
// slowPollInterval cadence and keeps trying indefinitely. Ready() flips to true
// the moment any fetch succeeds.
func NewJWTValidator(keycloakURL, realm string, logger *slog.Logger) *JWTValidator {
	if logger == nil {
		logger = slog.Default()
	}
	cleanURL := strings.TrimSuffix(keycloakURL, "/")
	v := &JWTValidator{
		logger:      logger,
		keycloakURL: cleanURL,
		issuerURL:   cleanURL, // default; override with SetIssuerURL if needed
		realm:       realm,
		publicKeys:  make(map[string]*rsa.PublicKey),
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}

	go v.initLoop()

	logger.Info("JWT validator created; initial JWKS fetch running in background",
		"keycloakURL", cleanURL, "realm", realm)
	return v
}

// SetIssuerURL overrides the URL used to validate the token's `iss` claim. Use
// it when the external Keycloak URL (written into tokens as `iss`) differs from
// the internal cluster URL used for JWKS fetching. An empty string is a no-op.
func (v *JWTValidator) SetIssuerURL(url string) {
	if url == "" {
		return
	}
	v.issuerURL = strings.TrimSuffix(url, "/")
}

// initLoop runs the initial JWKS fetch with exponential backoff, then falls
// back to a slow poll if all active attempts fail. It exits as soon as any
// fetch succeeds or when Stop() is called.
func (v *JWTValidator) initLoop() {
	defer close(v.doneCh)

	backoff := retryInitialBackoff
	for attempt := 1; attempt <= retryMaxAttempts; attempt++ {
		if v.stopped() {
			return
		}
		if err := v.fetchPublicKeys(); err == nil {
			v.ready.Store(true)
			v.logger.Info("JWT validator ready", "attempt", attempt)
			return
		} else {
			v.logger.Warn("failed to fetch Keycloak public keys, retrying",
				"attempt", attempt, "maxRetries", retryMaxAttempts, "backoff", backoff, "error", err,
				"hint", "verify LLM_KEYCLOAK_URL is correct — Keycloak 17+ does not use /auth as a context root")
		}
		if attempt < retryMaxAttempts {
			retryDelay(backoff)
			backoff *= 2
		}
	}

	// Active budget exhausted: switch to a steady slow poll so the validator
	// still comes online if Keycloak eventually recovers, without a tight retry
	// storm in the logs.
	v.logger.Warn("active retry budget exhausted; switching to slow poll", "interval", slowPollInterval)
	for {
		select {
		case <-v.stopCh:
			return
		case <-time.After(slowPollInterval):
		}
		if err := v.fetchPublicKeys(); err == nil {
			v.ready.Store(true)
			v.logger.Info("JWT validator ready (slow poll)")
			return
		} else {
			v.logger.Warn("slow poll JWKS fetch failed", "error", err)
		}
	}
}

func (v *JWTValidator) stopped() bool {
	select {
	case <-v.stopCh:
		return true
	default:
		return false
	}
}

// Ready reports whether the initial JWKS fetch has succeeded.
func (v *JWTValidator) Ready() bool {
	return v.ready.Load()
}

// Stop signals the background init goroutine to exit and waits for it. Safe to
// call multiple times, including concurrently.
func (v *JWTValidator) Stop() {
	v.stopOnce.Do(func() { close(v.stopCh) })
	<-v.doneCh
}

// ValidateToken verifies a bearer token's RSA signature, expiry (with
// clockLeeway) and issuer, returning the verified claims. It returns
// ErrNotReady if the initial JWKS fetch has not yet completed; the caller
// should surface that as 503 Service Unavailable.
func (v *JWTValidator) ValidateToken(tokenString string) (map[string]interface{}, error) {
	if !v.ready.Load() {
		return nil, ErrNotReady
	}

	if time.Since(v.lastFetch) > time.Hour {
		if err := v.fetchPublicKeys(); err != nil {
			v.logger.Error("failed to refresh public keys", "error", err)
		}
	}

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, fmt.Errorf("missing kid in token header")
		}

		v.keysMu.RLock()
		publicKey, exists := v.publicKeys[kid]
		v.keysMu.RUnlock()
		if exists {
			return publicKey, nil
		}

		// Key not cached — Keycloak may have rotated keys. Try a one-shot refresh.
		if refreshErr := v.fetchPublicKeys(); refreshErr != nil {
			return nil, fmt.Errorf("unknown key ID %s and key refresh failed: %w", kid, refreshErr)
		}
		v.keysMu.RLock()
		publicKey, exists = v.publicKeys[kid]
		v.keysMu.RUnlock()
		if !exists {
			return nil, fmt.Errorf("unknown key ID: %s (not found after key refresh)", kid)
		}
		return publicKey, nil
	}, jwt.WithLeeway(clockLeeway))
	if err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	// Expiry is already enforced by ParseWithClaims (with clockLeeway); only the
	// issuer needs a manual check. `aud`/`azp` are intentionally not checked, to
	// match the reference webapi validation.
	expectedIssuer := fmt.Sprintf("%s/realms/%s", v.issuerURL, v.realm)
	if iss, _ := claims["iss"].(string); iss != expectedIssuer {
		return nil, fmt.Errorf("invalid issuer: expected %s, got %s", expectedIssuer, iss)
	}

	return claims, nil
}

func (v *JWTValidator) fetchPublicKeys() error {
	certsURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/certs", v.keycloakURL, v.realm)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, certsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch keys: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			v.logger.Error("failed to close response body", "error", cerr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch keys: status %d", resp.StatusCode)
	}

	var set jwks
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return fmt.Errorf("failed to decode JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey)
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		publicKey, err := parseRSAPublicKey(k)
		if err != nil {
			v.logger.Error("failed to parse RSA public key", "kid", k.Kid, "error", err)
			continue
		}
		keys[k.Kid] = publicKey
	}

	if len(keys) == 0 {
		return fmt.Errorf("no valid RSA keys found")
	}

	v.keysMu.Lock()
	v.publicKeys = keys
	v.lastFetch = time.Now()
	v.keysMu.Unlock()

	v.logger.Info("Keycloak public keys refreshed", "count", len(keys))
	return nil
}

// parseRSAPublicKey rebuilds an RSA public key from a JWK's base64url modulus
// (n) and exponent (e).
func parseRSAPublicKey(k jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("failed to decode N: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("failed to decode E: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := 0
	for _, b := range eBytes {
		e = e*256 + int(b)
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}
