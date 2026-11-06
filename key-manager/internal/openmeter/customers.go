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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
	"github.com/nebari-dev/nebari-llm-serving-pack/shared/openmeterapi"
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

// ref names the customer in logs: its key, or its id if it has no key.
func (c storedCustomer) ref() string {
	for _, field := range []string{"key", "id"} {
		var v string
		if json.Unmarshal(c[field], &v) == nil && v != "" {
			return v
		}
	}
	return "(unnamed)"
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

// Client talks to the OpenMeter API.
type Client struct {
	*openmeterapi.Client
}

// NewClient returns a Client for the OpenMeter API at baseURL. token is sent
// as a bearer token when non-empty (OpenMeter's open-source server has no
// authentication; a proxy in front of it may).
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	return &Client{openmeterapi.New(baseURL, token, httpClient)}
}

func (c *Client) getCustomer(ctx context.Context, key string) (storedCustomer, error) {
	var out storedCustomer
	err := c.Get(ctx, "/api/v1/customers/"+url.PathEscape(key), nil, &out)
	return out, err
}

func (c *Client) createCustomer(ctx context.Context, cust customer) error {
	return c.Do(ctx, http.MethodPost, "/api/v1/customers", nil, cust, nil)
}

func (c *Client) replaceCustomer(ctx context.Context, key string, cust storedCustomer) error {
	return c.Do(ctx, http.MethodPut, "/api/v1/customers/"+url.PathEscape(key), nil, cust, nil)
}

// customersWithSubjectKey returns the customers holding subjectKey, by key
// (or id, for a customer with no key). OpenMeter's subject filter is a
// partial match, so the results are checked for the exact key.
func (c *Client) customersWithSubjectKey(ctx context.Context, subjectKey string) ([]string, error) {
	var holders []string
	for page := 1; ; page++ {
		var resp struct {
			TotalCount int              `json:"totalCount"`
			PageSize   int              `json:"pageSize"`
			Items      []storedCustomer `json:"items"`
		}
		q := url.Values{"subject": {subjectKey}, "page": {strconv.Itoa(page)}, "pageSize": {"100"}}
		if err := c.Get(ctx, "/api/v1/customers", q, &resp); err != nil {
			return nil, err
		}
		for _, item := range resp.Items {
			keys, err := item.subjectKeys()
			if err != nil {
				return nil, err
			}
			if slices.Contains(keys, subjectKey) {
				holders = append(holders, item.ref())
			}
		}
		if len(resp.Items) == 0 || page*resp.PageSize >= resp.TotalCount {
			return holders, nil
		}
	}
}

// KeyLister lists the metadata of every API key, and the owners of recently
// revoked ones (secrets.Manager).
type KeyLister interface {
	ListAllKeysIncludingRevoked(ctx context.Context) ([]secrets.KeyInfo, error)
}

// CustomerSync attaches API keys to their creators' OpenMeter customers.
type CustomerSync struct {
	keys   KeyLister
	client *Client
	logger *slog.Logger

	lastSkipped   int               // keys left out on the previous Sync, to warn only on change
	lastConflicts map[string]string // conflicts found by the previous Sync, to warn only on change
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
// all their keys' client IDs, revoked keys included (a key minted and
// revoked between two syncs is still attached). Subject keys are only ever
// added: a revoked key's past usage stays attributed. It is safe to run
// repeatedly.
//
// OpenMeter lets a subject key belong to one customer only. A client ID that
// another customer already holds (client IDs used to be reissued after a
// revoke, and whoever runs OpenMeter can attach any subject key) is left
// where it is and logged; the creator's customer gets the rest. Moving it
// would hand that customer's past usage under the ID to the key's creator,
// which only the operator can judge.
func (s *CustomerSync) Sync(ctx context.Context) error {
	keys, err := s.keys.ListAllKeysIncludingRevoked(ctx)
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
	conflicts := map[string]string{} // subject key -> customer holding it
	for subject, o := range owners {
		if err := s.syncOwner(ctx, subject, o, conflicts); err != nil {
			errs = append(errs, fmt.Errorf("customer %s: %w", subject, err))
		}
	}
	for k, holder := range conflicts {
		if s.lastConflicts[k] != holder {
			s.logger.Warn("openmeter: subject key is already on another customer, so it is not attached to its key creator's customer; move it in OpenMeter if the creator should be billed for usage under it",
				"subjectKey", k, "heldBy", holder)
		}
	}
	s.lastConflicts = conflicts
	return errors.Join(errs...)
}

// syncOwner adds the subject keys o's customer is missing, recording in
// conflicts each one another customer holds instead of adding it.
func (s *CustomerSync) syncOwner(ctx context.Context, subject string, o *owner, conflicts map[string]string) error {
	existing, err := s.client.getCustomer(ctx, subject)
	isNew := errors.Is(err, openmeterapi.ErrNotFound)
	if err != nil && !isNew {
		return err
	}
	have, err := existing.subjectKeys()
	if err != nil {
		return err
	}
	var add []string
	for _, k := range append([]string{subject}, o.clientIDs...) {
		if slices.Contains(have, k) {
			continue
		}
		holders, err := s.client.customersWithSubjectKey(ctx, k)
		if err != nil {
			return err
		}
		if len(holders) > 0 {
			if holders[0] != subject {
				conflicts[k] = holders[0]
			}
			continue
		}
		add = append(add, k)
	}
	if len(add) == 0 {
		return nil // nothing new
	}
	if isNew {
		slices.Sort(add)
		return s.client.createCustomer(ctx, customer{
			Key:              subject,
			Name:             o.username,
			UsageAttribution: usageAttribution{SubjectKeys: add},
		})
	}
	merged := append(slices.Clone(have), add...)
	slices.Sort(merged)
	if err := existing.setSubjectKeys(merged); err != nil {
		return err
	}
	return s.client.replaceCustomer(ctx, subject, existing)
}
