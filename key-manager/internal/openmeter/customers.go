// Package openmeter connects API keys to OpenMeter customers, so usage made
// with a key is attributed to the person who minted it.
//
// The gateway's usage events name the API key's client ID as their subject
// (requests with an OIDC token name the token's subject instead). An OpenMeter
// customer can hold several subject keys, and its usage is the sum over all of
// them. CustomerSync keeps one customer per key creator, keyed by the
// creator's OIDC subject, holding that subject and the client ID of every key
// the creator minted.
package openmeter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// customer is a new OpenMeter v1 customer, as the sync creates it.
type customer struct {
	Key              string           `json:"key"`
	Name             string           `json:"name"`
	UsageAttribution usageAttribution `json:"usageAttribution"`
}

type usageAttribution struct {
	SubjectKeys []string `json:"subjectKeys"`
}

// storedCustomer is an existing customer as OpenMeter returned it, every
// field kept. OpenMeter's PUT replaces the whole customer, and whoever runs
// OpenMeter may have set fields the sync knows nothing about (billing
// address, currency, metadata, ...), so the sync writes back exactly what it
// read with only the subject keys changed.
type storedCustomer map[string]json.RawMessage

func (c storedCustomer) subjectKeys() ([]string, error) {
	var ua usageAttribution
	if raw, ok := c["usageAttribution"]; ok {
		if err := json.Unmarshal(raw, &ua); err != nil {
			return nil, fmt.Errorf("reading usageAttribution: %w", err)
		}
	}
	return ua.SubjectKeys, nil
}

func (c storedCustomer) setSubjectKeys(keys []string) error {
	ua := map[string]json.RawMessage{}
	if raw, ok := c["usageAttribution"]; ok {
		if err := json.Unmarshal(raw, &ua); err != nil {
			return fmt.Errorf("reading usageAttribution: %w", err)
		}
	}
	b, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	ua["subjectKeys"] = b
	c["usageAttribution"], err = json.Marshal(ua)
	return err
}

// errNotFound is returned when OpenMeter has no customer with the given key.
var errNotFound = errors.New("customer not found")

// Client talks to the OpenMeter API.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient returns a Client for the OpenMeter API at baseURL. token is sent
// as a bearer token when non-empty (OpenMeter's open-source server has no
// authentication; a proxy in front of it may).
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimSuffix(baseURL, "/"), token: token, http: httpClient}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) getCustomer(ctx context.Context, key string) (storedCustomer, error) {
	var out storedCustomer
	err := c.do(ctx, http.MethodGet, "/api/v1/customers/"+url.PathEscape(key), nil, &out)
	return out, err
}

func (c *Client) createCustomer(ctx context.Context, cust customer) error {
	return c.do(ctx, http.MethodPost, "/api/v1/customers", cust, nil)
}

func (c *Client) replaceCustomer(ctx context.Context, key string, cust storedCustomer) error {
	return c.do(ctx, http.MethodPut, "/api/v1/customers/"+url.PathEscape(key), cust, nil)
}

// KeyLister lists the metadata of every API key (secrets.Manager).
type KeyLister interface {
	ListAllKeys(ctx context.Context) ([]secrets.KeyInfo, error)
}

// CustomerSync attaches API keys to their creators' OpenMeter customers.
type CustomerSync struct {
	keys   KeyLister
	client *Client
	logger *slog.Logger

	lastSkipped int // keys left out on the previous Sync, to warn only on change
}

// NewCustomerSync returns a CustomerSync. If logger is nil, slog.Default() is
// used.
func NewCustomerSync(keys KeyLister, client *Client, logger *slog.Logger) *CustomerSync {
	if logger == nil {
		logger = slog.Default()
	}
	return &CustomerSync{keys: keys, client: client, logger: logger}
}

// owner is one key creator and the client IDs of the keys they minted.
type owner struct {
	username  string
	clientIDs []string
}

// Sync makes sure every key creator has a customer holding their subject and
// all their keys' client IDs. Subject keys are only ever added: a revoked
// key's past usage stays attributed. It is safe to run repeatedly.
func (s *CustomerSync) Sync(ctx context.Context) error {
	keys, err := s.keys.ListAllKeys(ctx)
	if err != nil {
		return fmt.Errorf("listing API keys: %w", err)
	}
	owners := map[string]*owner{} // by creator subject
	skipped := 0
	for _, k := range keys {
		if k.CreatorSubject == "" {
			// Minted before creator subjects were recorded. A customer keyed
			// by username could later belong to someone else, so leave it out.
			skipped++
			continue
		}
		o := owners[k.CreatorSubject]
		if o == nil {
			o = &owner{username: k.Creator}
			owners[k.CreatorSubject] = o
		}
		o.clientIDs = append(o.clientIDs, k.ClientID)
	}

	if skipped != s.lastSkipped && skipped > 0 {
		s.logger.Warn("openmeter: API keys with no recorded creator subject are not attributed to a customer; re-mint them",
			"keys", skipped)
	}
	s.lastSkipped = skipped
	var errs []error
	for subject, o := range owners {
		if err := s.syncOwner(ctx, subject, o); err != nil {
			errs = append(errs, fmt.Errorf("customer %s: %w", subject, err))
		}
	}
	return errors.Join(errs...)
}

func (s *CustomerSync) syncOwner(ctx context.Context, subject string, o *owner) error {
	want := append([]string{subject}, o.clientIDs...)
	existing, err := s.client.getCustomer(ctx, subject)
	if errors.Is(err, errNotFound) {
		slices.Sort(want)
		return s.client.createCustomer(ctx, customer{
			Key:              subject,
			Name:             o.username,
			UsageAttribution: usageAttribution{SubjectKeys: want},
		})
	}
	if err != nil {
		return err
	}
	have, err := existing.subjectKeys()
	if err != nil {
		return err
	}
	merged := slices.Clone(have)
	for _, k := range want {
		if !slices.Contains(merged, k) {
			merged = append(merged, k)
		}
	}
	if len(merged) == len(have) {
		return nil // nothing new
	}
	slices.Sort(merged)
	if err := existing.setSubjectKeys(merged); err != nil {
		return err
	}
	return s.client.replaceCustomer(ctx, subject, existing)
}
