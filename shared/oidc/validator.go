// Package oidc checks OIDC access tokens (Keycloak's) for the pack's Go
// services: the key manager and the usage console verify bearer tokens the
// same way, against the provider's published keys.
package oidc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
)

// Defaults for the JWKS fetch retry/poll cadence, applied by New.
// The live values are per-instance fields on Validator (not package globals),
// so tests can tune them without racing under t.Parallel().
const (
	// defaultRetryMaxAttempts and defaultRetryInitialBackoff control the *active*
	// JWKS fetch retry loop on the background init goroutine. After this budget is
	// exhausted the goroutine switches to the slow poll to keep trying indefinitely
	// without the exponential blow-up.
	defaultRetryMaxAttempts    = 5
	defaultRetryInitialBackoff = 2 * time.Second
	// defaultSlowPollInterval is the cadence for the post-retry "keep trying" loop.
	defaultSlowPollInterval = 30 * time.Second
	// defaultKeyRefreshInterval is the steady-state cadence at which the background
	// goroutine proactively re-fetches the JWKS once the validator is ready, so a
	// key rotation is picked up without waiting for a request to miss the cache.
	// Kept deliberately off the request path (see refreshLoop): a synchronous
	// hourly refresh would stall every concurrent request for up to the fetch
	// timeout whenever Keycloak is unreachable at the moment the cache goes stale.
	defaultKeyRefreshInterval = time.Hour
)

// ErrNotReady is returned by ValidateToken when the validator's initial JWKS
// fetch has not completed yet. Callers should treat this as transient and
// surface a 503 Service Unavailable so clients distinguish "Keycloak not
// reachable yet" from "your token is bad" (401).
var ErrNotReady = errors.New("jwt validator: initial JWKS fetch not yet complete")

// ErrKeysUnavailable is returned by ValidateToken when a token's `kid` is not
// cached and the JWKS cannot be fetched (Keycloak unreachable, for example
// mid key rotation). Like ErrNotReady, callers should surface it as 503: the
// token may be fine, and signing in again would not help.
var ErrKeysUnavailable = errors.New("jwt validator: signing keys cannot be fetched")

// clockLeeway is the tolerance applied when validating the "exp" claim. A few
// seconds may elapse between the SPA minting the token and the request reaching
// the key-manager through nginx; without leeway, small clock drift causes
// spurious "token expired" errors. 30s is generous but still provides real
// expiry protection (Keycloak's default access-token lifetime is 5 minutes).
const clockLeeway = 30 * time.Second

// unknownKIDCooldown is the minimum interval between JWKS refreshes triggered by
// a token bearing an unrecognized `kid`. It exists purely to cap outbound load
// on Keycloak: because the gateway runs enforceAtGateway:false and nginx
// forwards Authorization as-is, an unauthenticated caller can send forged tokens
// with arbitrary `kid`s (this code runs *before* signature verification), and
// each cache miss would otherwise fan out into a 10s HTTP GET to Keycloak's
// /certs. singleflight collapses concurrent misses into one in-flight fetch, and
// this cooldown bounds the sustained refresh rate so request volume can't drive
// a fetch storm. Legitimate key rotations are rare and tolerate a 30s delay.
const unknownKIDCooldown = 30 * time.Second

// errKIDRefreshCooldown is returned when an unknown-`kid` refresh is suppressed
// because another refresh happened within unknownKIDCooldown.
var errKIDRefreshCooldown = errors.New("unknown-kid JWKS refresh suppressed by cooldown")

// maxJWKSBytes caps the JWKS response body read from Keycloak. A realm key set is
// a few KB; 1 MiB bounds memory against an oversized or unbounded response.
const maxJWKSBytes = 1 << 20

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

// Validator checks bearer tokens against the provider's JWKS: RSA signature,
// expiry (with clockLeeway, and required), the exact issuer, and optionally
// the client the token was issued to.
//
// The initial JWKS fetch runs asynchronously in a background goroutine started
// by New so process startup does not block on the provider being reachable
// (avoids CrashLoopBackOff when Keycloak is slow to come up). While ready is
// false, ValidateToken returns ErrNotReady, which callers surface as 503
// Service Unavailable, distinct from a bad token (401).
type Validator struct {
	logger *slog.Logger
	// keysURL is the JWKS URL; when empty, each fetch reads jwks_uri from
	// discoveryURL's OIDC discovery document.
	keysURL      string
	discoveryURL string
	// issuer is the exact `iss` a token must carry.
	issuer string
	// clientID, when non-empty, pins the token to one client: its `azp`
	// (authorized party) must be clientID, or with acceptAudience, `aud` may
	// name it instead. A Keycloak realm is shared across apps, so `iss` alone
	// would accept a token minted for any client in the realm.
	clientID       string
	acceptAudience bool
	publicKeys     map[string]*rsa.PublicKey
	keysMu         sync.RWMutex
	// refreshGroup collapses concurrent JWKS refreshes (periodic re-fetch and
	// unknown-kid re-fetch) into a single in-flight outbound request.
	refreshGroup singleflight.Group
	// lastKIDRefresh is the unix-nano timestamp of the most recent unknown-kid
	// refresh attempt, used to enforce unknownKIDCooldown. Atomic because it is
	// read and written from every request-handling goroutine.
	lastKIDRefresh atomic.Int64
	// fetchFailed is whether the most recent JWKS fetch failed, so a token
	// with an unknown kid during an outage is told apart from a forged one
	// even while the cooldown suppresses another fetch.
	fetchFailed atomic.Bool
	// ready flips to true once the first JWKS fetch succeeds. It is atomic
	// because the writer runs on the background init goroutine while readers
	// run on every request-handling goroutine.
	ready atomic.Bool
	// Retry/poll cadence, defaulted in New from the default* consts.
	// Instance fields rather than package globals so parallel tests can tune them
	// without racing. retryDelay is the sleep between active retries (swapped for a
	// no-op in tests).
	retryMaxAttempts    int
	retryInitialBackoff time.Duration
	slowPollInterval    time.Duration
	keyRefreshInterval  time.Duration
	retryDelay          func(time.Duration)
	// baseCtx bounds outbound JWKS fetches to the validator's lifetime; cancel is
	// invoked by Stop() so a shutdown aborts any in-flight fetch. Fetches use this
	// context, never a request's, so a single cancelled request cannot abort a
	// fetch that other in-flight requests are sharing via singleflight.
	baseCtx  context.Context
	cancel   context.CancelFunc
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

// Config is what a Validator checks tokens against.
type Config struct {
	// KeysURL is the provider's JWKS URL. Leave empty to read it from the
	// OIDC discovery document at DiscoveryURL (on every fetch).
	KeysURL      string
	DiscoveryURL string
	// Issuer is the exact `iss` tokens must carry. Required.
	Issuer string
	// ClientID, when set, must be the token's `azp`; with AcceptAudience, a
	// token whose `aud` names it is accepted too. Empty: any client.
	ClientID       string
	AcceptAudience bool
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// New creates a validator and returns immediately; the initial JWKS fetch
// runs on a background goroutine. It first runs retryMaxAttempts active
// attempts with exponential backoff, then falls back to a slowPollInterval
// cadence and keeps trying indefinitely. Ready() flips to true the moment any
// fetch succeeds.
func New(cfg Config) *Validator {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	v := &Validator{
		logger:              logger,
		keysURL:             cfg.KeysURL,
		discoveryURL:        strings.TrimSuffix(cfg.DiscoveryURL, "/"),
		issuer:              cfg.Issuer,
		clientID:            cfg.ClientID,
		acceptAudience:      cfg.AcceptAudience,
		publicKeys:          make(map[string]*rsa.PublicKey),
		retryMaxAttempts:    defaultRetryMaxAttempts,
		retryInitialBackoff: defaultRetryInitialBackoff,
		slowPollInterval:    defaultSlowPollInterval,
		keyRefreshInterval:  defaultKeyRefreshInterval,
		retryDelay:          time.Sleep,
		baseCtx:             baseCtx,
		cancel:              cancel,
		stopCh:              make(chan struct{}),
		doneCh:              make(chan struct{}),
	}

	go v.initLoop()

	logger.Info("JWT validator created; initial JWKS fetch running in background",
		"issuer", cfg.Issuer, "keysURL", cfg.KeysURL, "discoveryURL", cfg.DiscoveryURL)
	return v
}

// initLoop drives the validator's background goroutine: it first brings the
// validator online (initialFetch), then keeps the JWKS fresh on a steady cadence
// (refreshLoop). It exits, closing doneCh, only when Stop() is called.
func (v *Validator) initLoop() {
	defer close(v.doneCh)
	if v.initialFetch() {
		v.refreshLoop()
	}
}

// initialFetch runs the initial JWKS fetch with exponential backoff, then falls
// back to a slow poll if all active attempts fail. It returns true as soon as a
// fetch succeeds (marking the validator ready), or false if Stop() is called
// before that happens.
func (v *Validator) initialFetch() bool {
	backoff := v.retryInitialBackoff
	for attempt := 1; attempt <= v.retryMaxAttempts; attempt++ {
		if v.stopped() {
			return false
		}
		if err := v.fetchPublicKeys(v.baseCtx); err == nil {
			v.ready.Store(true)
			v.logger.Info("JWT validator ready", "attempt", attempt)
			return true
		} else {
			v.logger.Warn("failed to fetch Keycloak public keys, retrying",
				"attempt", attempt, "maxRetries", v.retryMaxAttempts, "backoff", backoff, "error", err,
				"hint", "check the provider URL: Keycloak 17+ does not use /auth as a context root")
		}
		if attempt < v.retryMaxAttempts {
			v.retryDelay(backoff)
			backoff *= 2
		}
	}

	// Active budget exhausted: switch to a steady slow poll so the validator
	// still comes online if Keycloak eventually recovers, without a tight retry
	// storm in the logs.
	v.logger.Warn("active retry budget exhausted; switching to slow poll", "interval", v.slowPollInterval)
	for {
		select {
		case <-v.stopCh:
			return false
		case <-time.After(v.slowPollInterval):
		}
		if err := v.fetchPublicKeys(v.baseCtx); err == nil {
			v.ready.Store(true)
			v.logger.Info("JWT validator ready (slow poll)")
			return true
		} else {
			v.logger.Warn("slow poll JWKS fetch failed", "error", err)
		}
	}
}

// refreshLoop proactively re-fetches the JWKS every keyRefreshInterval until
// Stop() is called, keeping key rotation off the request path. A failed refresh
// is logged and retried on the next tick; the previously-cached keys remain in
// use in the meantime, so a transient Keycloak outage never fails validation
// (fail-open on stale keys). The unknown-kid path in ValidateToken remains the
// fast trigger for a rotation that lands between ticks.
func (v *Validator) refreshLoop() {
	ticker := time.NewTicker(v.keyRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-v.stopCh:
			return
		case <-ticker.C:
			if err := v.refreshKeys(v.baseCtx); err != nil {
				v.logger.Error("background JWKS refresh failed; keeping cached keys", "error", err)
			}
		}
	}
}

func (v *Validator) stopped() bool {
	select {
	case <-v.stopCh:
		return true
	default:
		return false
	}
}

// Ready reports whether the initial JWKS fetch has succeeded.
func (v *Validator) Ready() bool {
	return v.ready.Load()
}

// Stop signals the background init goroutine to exit and waits for it. Safe to
// call multiple times, including concurrently.
func (v *Validator) Stop() {
	v.stopOnce.Do(func() {
		close(v.stopCh)
		v.cancel()
	})
	<-v.doneCh
}

// ValidateToken verifies a bearer token's RSA signature, expiry (with
// clockLeeway) and issuer, returning the verified claims. It returns
// ErrNotReady if the initial JWKS fetch has not yet completed, or
// ErrKeysUnavailable if the token's key is not cached and the JWKS cannot be
// fetched; the caller should surface either as 503 Service Unavailable. ctx cancels the caller's wait
// on any JWKS fetch triggered by an unknown `kid` (the fetch itself runs on the
// validator's lifetime context so one cancelled request can't abort a fetch
// shared with others).
func (v *Validator) ValidateToken(ctx context.Context, tokenString string) (map[string]interface{}, error) {
	if !v.ready.Load() {
		return nil, ErrNotReady
	}

	// The steady-state JWKS refresh runs on the background goroutine (refreshLoop),
	// not here, so a slow or unreachable Keycloak never stalls request handling.

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

		// Key not cached: Keycloak may have rotated keys. Trigger a rate-limited,
		// de-duplicated refresh, then re-check the cache regardless of the refresh
		// outcome. A concurrent burst with the same freshly-rotated kid all funnels
		// through the one in-flight fetch (singleflight) and finds the key here; a
		// caller whose refresh was suppressed by the cooldown may still find the key
		// populated by a fetch that just completed. Only a genuinely absent kid
		// (forged, or a bad token) falls through to the error below. See
		// unknownKIDCooldown / refreshForUnknownKID for why the refresh is guarded.
		_ = v.refreshForUnknownKID(ctx)
		v.keysMu.RLock()
		publicKey, exists = v.publicKeys[kid]
		v.keysMu.RUnlock()
		if !exists && v.fetchFailed.Load() {
			return nil, fmt.Errorf("unknown key ID %s: %w", kid, ErrKeysUnavailable)
		}
		if !exists {
			return nil, fmt.Errorf("unknown key ID: %s (not found after key refresh)", kid)
		}
		return publicKey, nil
	}, jwt.WithLeeway(clockLeeway), jwt.WithExpirationRequired())
	if err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	// Expiry is already enforced by ParseWithClaims (with clockLeeway); the issuer
	// and (optionally) the client are checked here.
	if iss, _ := claims["iss"].(string); iss != v.issuer {
		return nil, fmt.Errorf("invalid issuer: expected %s, got %s", v.issuer, iss)
	}

	// Public PKCE clients are not placed in `aud` by Keycloak, so `azp` is the
	// claim that identifies the client that obtained the token. A service
	// whose tokens name it as audience can accept that too (acceptAudience).
	if v.clientID != "" {
		azp, _ := claims["azp"].(string)
		aud, _ := claims.GetAudience()
		if azp != v.clientID && (!v.acceptAudience || !slices.Contains(aud, v.clientID)) {
			return nil, fmt.Errorf("token issued to %q, not %s", azp, v.clientID)
		}
	}

	return claims, nil
}

// refreshKeys performs a JWKS refresh, collapsing concurrent callers into a
// single in-flight outbound request via singleflight so a burst of requests
// arriving at the same time produces exactly one call to Keycloak. The fetch
// runs on the validator's lifetime context; ctx only bounds how long this caller
// waits for the shared result, so a cancelled request abandons its wait without
// aborting a fetch other in-flight requests are sharing.
func (v *Validator) refreshKeys(ctx context.Context) error {
	ch := v.refreshGroup.DoChan("certs", func() (interface{}, error) {
		return nil, v.fetchPublicKeys(v.baseCtx)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-ch:
		return res.Err
	}
}

// refreshForUnknownKID refreshes the JWKS in response to a token carrying an
// unrecognized `kid`, rate-limited to at most one outbound fetch per
// unknownKIDCooldown. This runs before signature verification on
// attacker-controllable input, so the cooldown plus singleflight is what prevents
// forged tokens with random `kid`s from amplifying request volume into a fetch
// storm against Keycloak. Returns errKIDRefreshCooldown when suppressed.
//
// The cooldown gate lives *inside* the singleflight callback on purpose. A burst
// of concurrent callers with the same legitimately-rotated kid collapses into one
// in-flight fetch: the winner runs the callback while the rest block, then every
// caller shares the result and re-checks the cache (see keyFunc). Were the gate
// outside singleflight, all-but-one caller would get a suppression error and 401
// even though the key they need is being fetched right then. The rate bound is
// unchanged: once a fetch has occurred, callbacks within the window return early
// without an outbound call, so request volume still cannot drive fetch volume.
func (v *Validator) refreshForUnknownKID(ctx context.Context) error {
	ch := v.refreshGroup.DoChan("certs", func() (interface{}, error) {
		now := time.Now().UnixNano()
		last := v.lastKIDRefresh.Load()
		if last != 0 && now-last < int64(unknownKIDCooldown) {
			return nil, errKIDRefreshCooldown
		}
		// Only one callback runs per in-flight singleflight window, so a plain
		// store (no CAS) is enough to stamp the window before fetching.
		v.lastKIDRefresh.Store(now)
		return nil, v.fetchPublicKeys(v.baseCtx)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-ch:
		return res.Err
	}
}

func (v *Validator) fetchPublicKeys(ctx context.Context) (err error) {
	defer func() { v.fetchFailed.Store(err != nil) }()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	certsURL := v.keysURL
	if certsURL == "" {
		var err error
		if certsURL, err = v.discoverKeysURL(ctx); err != nil {
			return err
		}
	}

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

	// Cap the response body: a realm JWKS is a few KB, so 1 MiB is a generous
	// ceiling that bounds memory if the endpoint (or something impersonating it)
	// returns an unexpectedly large or unbounded body.
	var set jwks
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&set); err != nil {
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
	v.keysMu.Unlock()

	v.logger.Info("Keycloak public keys refreshed", "count", len(keys))
	return nil
}

// discoverKeysURL reads jwks_uri from the provider's OIDC discovery document.
func (v *Validator) discoverKeysURL(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.discoveryURL+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create discovery request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to read discovery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to read discovery: status %d", resp.StatusCode)
	}
	var disc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&disc); err != nil {
		return "", fmt.Errorf("failed to decode discovery: %w", err)
	}
	if disc.JWKSURI == "" {
		return "", errors.New("discovery has no jwks_uri")
	}
	return disc.JWKSURI, nil
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
