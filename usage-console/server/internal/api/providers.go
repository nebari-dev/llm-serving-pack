package api

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/routing"
	"github.com/jbouder/stargate/server/internal/store"
)

// Providers (spec §7.5.1, §7.5.6, §9.1): backends are desired state like
// routes, written with audit rows and If-Match, and reach the gateway on the
// next apply. A provider key goes to the KeyStore and nowhere else: Postgres
// keeps its reference and prefix, and no response carries it. Testing a
// connection lists the provider's models with the key.

// BackendInput is a backend write: the console's provider form. APIKey is
// read on create and never written back.
type BackendInput struct {
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Region   string   `json:"region"`
	BaseURL  string   `json:"baseUrl"`
	Models   []string `json:"models"`
	APIKey   string   `json:"apiKey"`
	// LiteLLMKeys links models to LiteLLM entries on create (model → key),
	// as GET /pricing/litellm/match offers them.
	LiteLLMKeys map[string]string `json:"litellmKeys"`
}

// BackendResult is a backend after a write, with the connection test the
// write ran (create, set key, test).
type BackendResult struct {
	Backend model.Backend           `json:"backend"`
	Test    *routing.ConnectionTest `json:"test,omitempty"`
	// Pricing says what didn't happen when linking LiteLLM entries on
	// create; empty when they're linked and priced.
	Pricing string `json:"pricing,omitempty"`
}

func (s *Server) providerClient() *http.Client { return http.DefaultClient }

// readBackend decodes and checks a backend write against was (nil on create).
func readBackend(r *http.Request, name string, was *model.Backend) (model.Backend, string, error) {
	b, key, _, err := readBackendInput(r, name, was)
	return b, key, err
}

// readBackendInput is readBackend with the LiteLLM links a create may carry.
func readBackendInput(r *http.Request, name string, was *model.Backend) (model.Backend, string, map[string]string, error) {
	var in BackendInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return model.Backend{}, "", nil, badRequest("invalid JSON body")
	}
	b, key, err := checkBackendInput(in, name, was)
	return b, key, in.LiteLLMKeys, err
}

func checkBackendInput(in BackendInput, name string, was *model.Backend) (model.Backend, string, error) {
	if name != "" {
		in.Name = name
	}
	b := model.Backend{Name: strings.TrimSpace(in.Name), Provider: strings.TrimSpace(in.Provider), Region: strings.TrimSpace(in.Region)}
	for _, m := range in.Models {
		b.Models = append(b.Models, strings.TrimSpace(m))
	}
	e, err := routing.ParseBaseURL(in.BaseURL)
	if err != nil {
		return b, "", badRequest(err.Error())
	}
	b.Endpoint = &e
	if err := routing.ValidateBackend(b, was); err != nil {
		return b, "", badRequest(err.Error())
	}
	if in.APIKey != "" {
		if err := routing.ValidateKey(in.APIKey); err != nil {
			return b, "", badRequest(err.Error())
		}
	}
	return b, in.APIKey, nil
}

// backendView is one backend as GET /backends shows it.
func (s *Server) backendView(w http.ResponseWriter, r *http.Request, t, name string) (model.Backend, error) {
	v, err := s.backends(w, r, t)
	if err != nil {
		return model.Backend{}, err
	}
	bs := v.([]model.Backend)
	i := slices.IndexFunc(bs, func(b model.Backend) bool { return b.Name == name })
	if i < 0 {
		return model.Backend{}, store.ErrNotFound
	}
	w.Header().Set("ETag", bs[i].ETag)
	return bs[i], nil
}

func (s *Server) keyStore() (routing.KeyStore, error) {
	if s.Keys == nil {
		return nil, unavailable("No key store is configured, so provider keys can't be saved (stargate-api serve -provider-keys).")
	}
	return s.Keys, nil
}

// connect tests a backend's connection with key (none when "").
func (s *Server) connect(ctx context.Context, b model.Backend, key string) routing.ConnectionTest {
	return routing.TestConnection(ctx, s.providerClient(), b.Provider, routing.ResolvedBaseURL(*b.Endpoint, os.Getenv), key)
}

// record keeps a saved backend's connection test.
func (s *Server) record(ctx context.Context, t, name string, res routing.ConnectionTest) error {
	return s.Store.RecordBackendTest(ctx, t, name, model.BackendTest{At: res.At, OK: res.OK, Message: res.Message()})
}

// test runs a saved backend's connection test with key and keeps the result.
func (s *Server) test(ctx context.Context, t string, b model.Backend, key string) (routing.ConnectionTest, error) {
	res := s.connect(ctx, b, key)
	return res, s.record(ctx, t, b.Name, res)
}

// testKey is a new key's one test (spec §7.5.1 "Tested once, then sealed"):
// a key that fails it is refused before anything is written.
func (s *Server) testKey(ctx context.Context, b model.Backend, key string) (routing.ConnectionTest, error) {
	res := s.connect(ctx, b, key)
	if !res.OK {
		return res, keyRefused("The key was not saved: its connection test failed. Check the key and the base URL, then try again.\n" + res.Error)
	}
	return res, nil
}

// createBackend takes {name, provider, region, baseUrl, models, apiKey?,
// litellmKeys?}. A key is tested first, and the backend saved with it only if
// it passes; a backend with no key is saved, then tested (a self-hosted one
// may be down). litellmKeys are checked before anything is saved, then each
// is linked (its own audit row) and prices sync once.
func (s *Server) createBackend(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	b, key, links, err := readBackendInput(r, "", nil)
	if err != nil {
		return nil, err
	}
	if err := s.checkLiteLLMKeys(r.Context(), b.Models, links); err != nil {
		return nil, err
	}
	var nk *store.NewProviderKey
	var res routing.ConnectionTest
	if key != "" {
		ks, err := s.keyStore()
		if err != nil {
			return nil, err
		}
		if res, err = s.testKey(r.Context(), b, key); err != nil {
			return nil, err
		}
		ref := routing.KeyRef(b.Name)
		nk = &store.NewProviderKey{Ref: ref, Prefix: routing.KeyPrefix(key), Put: func() error { return ks.Put(ref, key) }}
	}
	saved, err := s.Store.CreateBackend(r.Context(), t, actor(r), b, nk)
	if err != nil {
		return nil, err
	}
	s.configChanged()
	if key == "" {
		res, err = s.test(r.Context(), t, saved, "")
	} else {
		err = s.record(r.Context(), t, saved.Name, res)
	}
	if err != nil {
		return nil, err
	}
	out := BackendResult{Test: &res}
	if len(links) > 0 {
		out.Pricing = s.linkPrices(r.Context(), t, actor(r), saved.Name, links)
	}
	out.Backend, err = s.backendView(w, r, t, saved.Name)
	return out, err
}

// linkPrices links a new backend's models to their LiteLLM entries and
// syncs, so they're priced at once. The backend is saved either way; what
// didn't happen comes back as a note rather than failing the save.
func (s *Server) linkPrices(ctx context.Context, t, who, backend string, links map[string]string) string {
	for _, m := range slices.Sorted(maps.Keys(links)) {
		if err := s.Store.SetPriceSource(ctx, t, who, m, backend, links[m]); err != nil {
			return fmt.Sprintf("Saved, but linking %s to LiteLLM failed: %v. Link it on Models.", m, err)
		}
	}
	if err := s.SyncPrices(ctx); err != nil {
		return "Linked to LiteLLM; prices arrive with the next sync (this one failed: " + err.Error() + ")."
	}
	return ""
}

// updateBackend replaces a backend's provider, region, base URL and models.
// The name is fixed, and so is the key: PUT …/key replaces it.
func (s *Server) updateBackend(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	was, err := s.Store.Backend(r.Context(), t, r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	b, key, err := readBackend(r, was.Name, &was)
	if err != nil {
		return nil, err
	}
	if key != "" {
		return nil, badRequest("replace the key on its own (PUT /backends/{name}/key)")
	}
	if _, err := s.Store.UpdateBackend(r.Context(), t, actor(r), m, b); err != nil {
		return nil, err
	}
	s.configChanged()
	return s.backendView(w, r, t, b.Name)
}

// deleteBackend removes a backend no route sends to, and its key.
func (s *Server) deleteBackend(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	name := r.PathValue("name")
	was, err := s.Store.DeleteBackend(r.Context(), t, actor(r), name, m)
	if err != nil {
		return nil, err
	}
	if was.Key != nil && s.Keys != nil {
		// Only a key the console stored: a reference set by hand (server/.env) stays.
		if err := s.Keys.Remove(was.Endpoint.APIKeyEnv); err != nil {
			return nil, err
		}
	}
	s.configChanged()
	return map[string]string{"name": name}, nil
}

// setBackendKey takes {"apiKey": "…"}: tests the connection with it and, if
// that passes, stages it under the backend's reference and records its
// prefix; a key that fails is refused and nothing changes. Like the other key
// writes it takes no If-Match (decisions §7). The gateway sends the new key
// once routing is applied.
func (s *Server) setBackendKey(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	if err := routing.ValidateKey(in.APIKey); err != nil {
		return nil, badRequest(err.Error())
	}
	ks, err := s.keyStore()
	if err != nil {
		return nil, err
	}
	was, err := s.Store.Backend(r.Context(), t, r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	if was.Endpoint == nil {
		return nil, conflict(was.Name + " has no endpoint, so the gateway has nowhere to send a key")
	}
	res, err := s.testKey(r.Context(), was, in.APIKey)
	if err != nil {
		return nil, err
	}
	ref := routing.KeyRef(was.Name)
	if was.Endpoint.APIKeyEnv != "" {
		ref = was.Endpoint.APIKeyEnv
	}
	saved, err := s.Store.SetBackendKey(r.Context(), t, actor(r), was.Name, store.NewProviderKey{
		Ref: ref, Prefix: routing.KeyPrefix(in.APIKey), Put: func() error { return ks.Put(ref, in.APIKey) },
	})
	if err != nil {
		return nil, err
	}
	s.configChanged()
	if err := s.record(r.Context(), t, saved.Name, res); err != nil {
		return nil, err
	}
	v, err := s.backendView(w, r, t, saved.Name)
	return BackendResult{Backend: v, Test: &res}, err
}

// testBackend tests a saved backend with its stored key, and keeps the result.
func (s *Server) testBackend(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	b, err := s.Store.Backend(r.Context(), t, r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	if b.Endpoint == nil {
		return nil, conflict(b.Name + " has no endpoint to test")
	}
	key := ""
	if ref := b.Endpoint.APIKeyEnv; ref != "" {
		ks, err := s.keyStore()
		if err != nil {
			return nil, err
		}
		k, ok, err := ks.Get(ref)
		if err != nil {
			return nil, err
		}
		if !ok {
			res := routing.ConnectionTest{Models: []string{}, Error: "No key is stored under " + ref + ", so the gateway sends \"not-set\". Set the key."}
			return BackendResult{Backend: b, Test: &res}, nil
		}
		key = k
	}
	res, err := s.test(r.Context(), t, b, key)
	if err != nil {
		return nil, err
	}
	v, err := s.backendView(w, r, t, b.Name)
	return BackendResult{Backend: v, Test: &res}, err
}

// testProvider tests a provider before it's saved: {provider, baseUrl,
// apiKey?}. The key is used for this request only; nothing is stored.
func (s *Server) testProvider(_ http.ResponseWriter, r *http.Request, _ string) (any, error) {
	var in BackendInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	provider := strings.TrimSpace(in.Provider)
	if slices.Contains(routing.CloudProviders, provider) {
		return nil, badRequest(provider + " needs cloud credentials, which the console can't set up yet")
	}
	if _, err := routing.ParseBaseURL(in.BaseURL); err != nil {
		return nil, badRequest(err.Error())
	}
	if in.APIKey != "" {
		if err := routing.ValidateKey(in.APIKey); err != nil {
			return nil, badRequest(err.Error())
		}
	}
	return routing.TestConnection(r.Context(), s.providerClient(), provider, strings.TrimSpace(in.BaseURL), in.APIKey), nil
}
