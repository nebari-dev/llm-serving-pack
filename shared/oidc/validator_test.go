package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer = "https://keycloak.example.com/realms/nebari"
	testKID    = "test-key-1"
)

// newTestValidator returns a Validator preloaded with pub under testKID and
// marked ready, so tests validate signed tokens without any network fetch.
func newTestValidator(t *testing.T, pub *rsa.PublicKey) *Validator {
	t.Helper()
	v := &Validator{
		logger:     slog.Default(),
		issuer:     testIssuer,
		publicKeys: map[string]*rsa.PublicKey{testKID: pub},
		baseCtx:    context.Background(),
	}
	v.ready.Store(true)
	return v
}

// signToken signs claims as an RS256 JWT with testKID. iss/exp are filled with
// valid defaults unless already present.
func signToken(t *testing.T, priv *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	return signTokenWithKID(t, priv, testKID, claims)
}

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	return key
}

// writeJWKS encodes pub as a single-key JWKS under kid, matching the shape the
// validator's fetchPublicKeys expects from Keycloak's /certs endpoint.
func writeJWKS(t *testing.T, w http.ResponseWriter, kid string, pub *rsa.PublicKey) {
	t.Helper()
	set := jwks{Keys: []jwk{{
		Kty: "RSA",
		Kid: kid,
		Use: "sig",
		N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}}
	if err := json.NewEncoder(w).Encode(set); err != nil {
		t.Fatalf("encoding jwks: %v", err)
	}
}

// signTokenWithKID signs claims with an arbitrary `kid` header so tests can
// forge tokens whose key ID is not in the validator's cache.
func signTokenWithKID(t *testing.T, priv *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	if _, ok := claims["iss"]; !ok {
		claims["iss"] = testIssuer
	}
	if v, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(5 * time.Minute).Unix()
	} else if v == nil {
		delete(claims, "exp") // a token with no expiry at all
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return s
}

// TestUnknownKIDRefreshIsRateLimited guards against DoS amplification: a caller
// sending tokens with unrecognized `kid`s (verifiable before signature check,
// since the gateway forwards Authorization as-is under enforceAtGateway:false)
// must not turn each request into an outbound JWKS fetch. singleflight collapses
// a concurrent burst into one fetch, and unknownKIDCooldown suppresses the rest.
func TestUnknownKIDRefreshIsRateLimited(t *testing.T) {
	priv := genKey(t)

	var fetchCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetchCount.Add(1)
		writeJWKS(t, w, testKID, &priv.PublicKey)
	}))
	defer srv.Close()

	v := &Validator{
		logger:     slog.Default(),
		keysURL:    srv.URL,
		issuer:     testIssuer,
		publicKeys: map[string]*rsa.PublicKey{testKID: &priv.PublicKey},
		baseCtx:    context.Background(),
	}
	v.ready.Store(true)

	// The server never returns "unknown-kid", so every attempt is a genuine cache
	// miss that would fetch if unguarded.
	forged := signTokenWithKID(t, priv, "unknown-kid", jwt.MapClaims{"preferred_username": "mallory"})

	const burst = 50
	var wg sync.WaitGroup
	for range burst {
		wg.Go(func() {
			// All of these must fail (kid never resolves); we only care about fetches.
			_, _ = v.ValidateToken(context.Background(), forged)
		})
	}
	wg.Wait()

	if got := fetchCount.Load(); got != 1 {
		t.Fatalf("expected exactly 1 JWKS fetch for a burst of %d unknown-kid tokens, got %d", burst, got)
	}

	// A further attempt inside the cooldown window must not fetch again.
	_, _ = v.ValidateToken(context.Background(), forged)
	if got := fetchCount.Load(); got != 1 {
		t.Fatalf("expected cooldown to suppress refresh, got %d fetches", got)
	}

	// Once the cooldown has elapsed (simulated by clearing the timestamp), a new
	// unknown-kid attempt is allowed to refresh again.
	v.lastKIDRefresh.Store(0)
	_, _ = v.ValidateToken(context.Background(), forged)
	if got := fetchCount.Load(); got != 2 {
		t.Fatalf("expected a refresh after cooldown elapsed, got %d fetches", got)
	}
}

// TestRotatedKIDResolvesForConcurrentBurst is the rotation counterpart to
// TestUnknownKIDRefreshIsRateLimited. When Keycloak has rotated to a new signing
// key, a concurrent burst of legitimate requests bearing the new kid must all
// validate off a single shared JWKS fetch; none may be spuriously 401'd by the
// cooldown gate. This is the case the SPA hits on the first page load after a
// rotation, firing /api/me, /api/models and /api/keys concurrently with the same
// fresh token. The rate bound (exactly one outbound fetch) must still hold.
func TestRotatedKIDResolvesForConcurrentBurst(t *testing.T) {
	oldPriv := genKey(t)
	rotPriv := genKey(t)
	const rotatedKID = "rotated-kid"

	var fetchCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetchCount.Add(1)
		writeJWKS(t, w, rotatedKID, &rotPriv.PublicKey)
	}))
	defer srv.Close()

	// Validator starts holding only the pre-rotation key, under testKID.
	v := &Validator{
		logger:     slog.Default(),
		keysURL:    srv.URL,
		issuer:     testIssuer,
		publicKeys: map[string]*rsa.PublicKey{testKID: &oldPriv.PublicKey},
		baseCtx:    context.Background(),
	}
	v.ready.Store(true)

	// A genuine token minted by the rotated key; its kid is not yet cached.
	token := signTokenWithKID(t, rotPriv, rotatedKID, jwt.MapClaims{"preferred_username": "alice"})

	const burst = 50
	var wg sync.WaitGroup
	var okCount atomic.Int64
	for range burst {
		wg.Go(func() {
			if _, err := v.ValidateToken(context.Background(), token); err == nil {
				okCount.Add(1)
			}
		})
	}
	wg.Wait()

	if got := okCount.Load(); got != burst {
		t.Fatalf("expected all %d concurrent requests with the rotated kid to validate, got %d", burst, got)
	}
	if got := fetchCount.Load(); got != 1 {
		t.Fatalf("expected exactly 1 JWKS fetch for the rotation burst, got %d", got)
	}
}

// TestAzpValidation covers the opt-in `azp` pin: when an expected client ID is
// set, only tokens whose `azp` matches are accepted; when unset, `azp` is ignored.
func TestAzpValidation(t *testing.T) {
	priv := genKey(t)

	tests := []struct {
		name             string
		expectedClientID string
		azp              interface{} // nil = omit the claim
		wantErr          bool
	}{
		{"no pin accepts matching azp", "", "key-manager-spa", false},
		{"no pin accepts foreign azp", "", "some-other-client", false},
		{"no pin accepts missing azp", "", nil, false},
		{"pin accepts matching azp", "key-manager-spa", "key-manager-spa", false},
		{"pin rejects foreign azp", "key-manager-spa", "some-other-client", true},
		{"pin rejects missing azp", "key-manager-spa", nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestValidator(t, &priv.PublicKey)
			v.clientID = tc.expectedClientID

			claims := jwt.MapClaims{"preferred_username": "alice"}
			if tc.azp != nil {
				claims["azp"] = tc.azp
			}
			_, err := v.ValidateToken(context.Background(), signToken(t, priv, claims))

			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

// TestKnownKIDNeverRefetches confirms the happy path is fetch-free: a token
// whose kid is already cached validates without any outbound call.
func TestKnownKIDNeverRefetches(t *testing.T) {
	priv := genKey(t)

	var fetchCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetchCount.Add(1)
		writeJWKS(t, w, testKID, &priv.PublicKey)
	}))
	defer srv.Close()

	v := &Validator{
		logger:     slog.Default(),
		keysURL:    srv.URL,
		issuer:     testIssuer,
		publicKeys: map[string]*rsa.PublicKey{testKID: &priv.PublicKey},
		baseCtx:    context.Background(),
	}
	v.ready.Store(true)

	if _, err := v.ValidateToken(context.Background(), signToken(t, priv, jwt.MapClaims{"preferred_username": "alice"})); err != nil {
		t.Fatalf("validating a token with a cached kid: %v", err)
	}
	if got := fetchCount.Load(); got != 0 {
		t.Fatalf("expected no JWKS fetch for a cached kid, got %d", got)
	}
}

// TestInitialFetchRetriesThenSucceeds covers the active retry loop: the first two
// attempts fail and the third succeeds within the retry budget, so the validator
// comes ready without falling through to the slow poll. retryDelay is a counter
// so the test does not sleep.
func TestInitialFetchRetriesThenSucceeds(t *testing.T) {
	priv := genKey(t)
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJWKS(t, w, testKID, &priv.PublicKey)
	}))
	defer srv.Close()

	var delays int
	v := &Validator{
		logger:              slog.Default(),
		keysURL:             srv.URL,
		issuer:              testIssuer,
		publicKeys:          map[string]*rsa.PublicKey{},
		retryMaxAttempts:    5,
		retryInitialBackoff: time.Millisecond,
		slowPollInterval:    time.Millisecond,
		retryDelay:          func(time.Duration) { delays++ },
		baseCtx:             context.Background(),
	}

	if ok := v.initialFetch(); !ok {
		t.Fatal("expected initialFetch to succeed once the server recovers")
	}
	if !v.Ready() {
		t.Error("validator should be ready after a successful fetch")
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("expected 3 fetch attempts (2 failures + 1 success), got %d", got)
	}
	if delays != 2 {
		t.Errorf("expected 2 backoff delays between the 3 attempts, got %d", delays)
	}
}

// TestInitialFetchFallsBackToSlowPoll covers the state transition after the
// active retry budget is exhausted: all active attempts fail, and a later slow
// poll succeeds and marks the validator ready.
func TestInitialFetchFallsBackToSlowPoll(t *testing.T) {
	priv := genKey(t)
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) <= 2 { // both active attempts fail
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJWKS(t, w, testKID, &priv.PublicKey)
	}))
	defer srv.Close()

	v := &Validator{
		logger:              slog.Default(),
		keysURL:             srv.URL,
		issuer:              testIssuer,
		publicKeys:          map[string]*rsa.PublicKey{},
		retryMaxAttempts:    2,
		retryInitialBackoff: time.Millisecond,
		slowPollInterval:    time.Millisecond,
		retryDelay:          func(time.Duration) {},
		baseCtx:             context.Background(),
		stopCh:              make(chan struct{}),
	}

	if ok := v.initialFetch(); !ok {
		t.Fatal("expected initialFetch to eventually succeed via the slow poll")
	}
	if !v.Ready() {
		t.Error("validator should be ready after a slow-poll success")
	}
}

// With acceptAudience, a token whose aud names the client is accepted even
// when another client obtained it; without, only azp counts.
func TestClientInAudience(t *testing.T) {
	priv := genKey(t)
	tok := signToken(t, priv, jwt.MapClaims{"azp": "gateway", "aud": []string{"account", "usage-console"}})
	for _, accept := range []bool{true, false} {
		v := newTestValidator(t, &priv.PublicKey)
		v.clientID, v.acceptAudience = "usage-console", accept
		if _, err := v.ValidateToken(context.Background(), tok); (err == nil) != accept {
			t.Errorf("acceptAudience=%v: err %v", accept, err)
		}
	}
}

// Without a keys URL, the keys come from the provider's discovery document.
func TestKeysFromDiscovery(t *testing.T) {
	priv := genKey(t)
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /realms/nebari/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode(map[string]string{"jwks_uri": srv.URL + "/certs"}); err != nil {
			t.Error(err)
		}
	})
	mux.HandleFunc("GET /certs", func(w http.ResponseWriter, _ *http.Request) { writeJWKS(t, w, testKID, &priv.PublicKey) })
	srv = httptest.NewServer(mux)
	defer srv.Close()

	v := New(Config{DiscoveryURL: srv.URL + "/realms/nebari/", Issuer: testIssuer})
	defer v.Stop()
	for deadline := time.Now().Add(5 * time.Second); !v.Ready(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("never ready")
		}
	}
	if _, err := v.ValidateToken(context.Background(), signToken(t, priv, jwt.MapClaims{})); err != nil {
		t.Fatal(err)
	}
}

// Tokens refused: each differs from a good one in one way.
func TestValidateTokenRefuses(t *testing.T) {
	priv, other := genKey(t), genKey(t)
	v := newTestValidator(t, &priv.PublicKey)
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": testIssuer, "exp": time.Now().Add(time.Minute).Unix()})
	hs.Header["kid"] = testKID
	hsTok, _ := hs.SignedString([]byte("secret"))
	noExp := jwt.MapClaims{"exp": nil}
	for name, tok := range map[string]string{
		"expired":       signToken(t, priv, jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()}),
		"no expiry":     signToken(t, priv, noExp),
		"not yet valid": signToken(t, priv, jwt.MapClaims{"nbf": time.Now().Add(time.Hour).Unix()}),
		"other issuer":  signToken(t, priv, jwt.MapClaims{"iss": "https://keycloak.example.com/realms/other"}),
		"other key":     signToken(t, other, jwt.MapClaims{}),
		"HMAC":          hsTok,
		"not a JWT":     "abc",
	} {
		if _, err := v.ValidateToken(context.Background(), tok); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A failed background refresh keeps the keys already read: a Keycloak
// outage doesn't lock everyone out.
func TestFailedRefreshKeepsKnownKeys(t *testing.T) {
	priv := genKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	v := newTestValidator(t, &priv.PublicKey)
	v.keysURL = srv.URL

	if err := v.refreshKeys(context.Background()); err == nil {
		t.Fatal("refresh against a down provider succeeded")
	}
	if _, err := v.ValidateToken(context.Background(), signToken(t, priv, jwt.MapClaims{})); err != nil {
		t.Fatalf("known key refused after a failed refresh: %v", err)
	}
}
