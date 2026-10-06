package gateway

import (
	"context"
	"log"
	"maps"
	"slices"
	"sync/atomic"
	"time"

	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// LoadSnapshot reads everything the gateway enforces for one tenant. In the
// full design Warden receives this as a pushed, versioned snapshot (§4.1);
// here the gateway polls.
func LoadSnapshot(ctx context.Context, st *store.Store, tenant string) (*Snapshot, error) {
	s := &Snapshot{Tenant: tenant, KeyBy: map[string]*store.KeyRecord{}, Models: map[string]model.Model{}, Prices: map[Pair][]store.PriceRow{}, Budgets: map[string]model.Budget{}}
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
	// Rows from a day back, so a record that arrives late still finds the
	// price in effect when its request started.
	prices, err := st.PriceRowsSince(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	s.Prices = s.WithPrices(prices).Prices
	if s.Aliases, err = st.Aliases(ctx, tenant); err != nil {
		return nil, err
	}
	if s.Backends, err = st.Backends(ctx, tenant); err != nil {
		return nil, err
	}
	if s.Routes, err = st.Routes(ctx, tenant); err != nil {
		return nil, err
	}
	if s.RunningRoutes, err = st.RunningRoutes(ctx, tenant); err != nil {
		return nil, err
	}
	budgets, err := st.Budgets(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, b := range budgets {
		s.Budgets[b.ID] = b
	}
	if s.Policies, err = st.Policies(ctx, tenant); err != nil {
		return nil, err
	}
	if s.Spend, err = st.MonthToDate(ctx, tenant); err != nil {
		return nil, err
	}
	custom, err := st.CustomEntities(ctx, tenant)
	if err != nil {
		return nil, err
	}
	// An entity that no longer passes the pattern check is left out (its
	// rules then match nothing) and logged; the rest still load.
	if s.Detectors, err = NewDetectors(custom); err != nil {
		log.Printf("snapshot %s: %v", tenant, err)
	}
	return s, nil
}

// Current holds the latest snapshot for concurrent readers.
type Current struct{ p atomic.Pointer[Snapshot] }

func (c *Current) Load() *Snapshot   { return c.p.Load() }
func (c *Current) Store(s *Snapshot) { c.p.Store(s) }

// DemoSnapshot is the seeded demo config without a database, for tests.
func DemoSnapshot() *Snapshot {
	s := &Snapshot{Tenant: demo.Tenant, KeyBy: map[string]*store.KeyRecord{}, Models: map[string]model.Model{}, Prices: map[Pair][]store.PriceRow{}, Budgets: map[string]model.Budget{},
		Aliases: maps.Clone(demo.Aliases), Backends: slices.Clone(demo.Backends), Routes: slices.Clone(demo.Routes), Policies: slices.Clone(demo.Policies),
		Spend: store.MonthSpend{ByTeam: map[string]float64{}, ByKey: map[string]float64{}}}
	for _, k := range demo.Keys {
		rec := &store.KeyRecord{APIKey: k, Hash: demo.HashSecret(demo.DevSecret(k.Prefix))}
		s.KeyBy[rec.Hash] = rec
	}
	for _, m := range demo.Models {
		s.Models[m.ID] = m
	}
	for _, p := range demo.SeedPrices(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		k := Pair{p.ModelID, p.Backend}
		s.Prices[k] = append(s.Prices[k], store.PriceRow{ModelID: p.ModelID, Backend: p.Backend, Rates: p.Rates, Sources: p.Sources, From: p.From})
	}
	for _, b := range demo.Budgets {
		s.Budgets[b.ID] = b
	}
	return s
}
