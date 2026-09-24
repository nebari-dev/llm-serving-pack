package gateway

import (
	"context"
	"sync/atomic"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// LoadSnapshot reads everything the gateway enforces for one tenant. In the
// full design Warden receives this as a pushed, versioned snapshot (§4.1);
// here the gateway polls.
func LoadSnapshot(ctx context.Context, st *store.Store, tenant string) (*Snapshot, error) {
	s := &Snapshot{Tenant: tenant, KeyBy: map[string]*store.KeyRecord{}, Models: map[string]model.Model{}, Budgets: map[string]model.Budget{}}
	keys, err := st.Keys(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		k := &keys[i]
		s.KeyBy[k.Hash] = k
		if k.NextHash != "" {
			s.KeyBy[k.NextHash] = k
		}
	}
	models, err := st.Models(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		s.Models[m.ID] = m
	}
	if s.Aliases, err = st.Aliases(ctx); err != nil {
		return nil, err
	}
	if s.Backends, err = st.Backends(ctx, tenant); err != nil {
		return nil, err
	}
	if s.Routes, err = st.Routes(ctx, tenant); err != nil {
		return nil, err
	}
	budgets, err := st.Budgets(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, b := range budgets {
		s.Budgets[b.ID] = b
	}
	if s.Rules, err = st.Rules(ctx, tenant); err != nil {
		return nil, err
	}
	if s.Spend, err = st.MonthToDate(ctx, tenant); err != nil {
		return nil, err
	}
	return s, nil
}

// Current holds the latest snapshot for concurrent readers.
type Current struct{ p atomic.Pointer[Snapshot] }

func (c *Current) Load() *Snapshot   { return c.p.Load() }
func (c *Current) Store(s *Snapshot) { c.p.Store(s) }
