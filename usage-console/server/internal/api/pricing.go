package api

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/pricing"
	"github.com/jbouder/stargate/server/internal/store"
)

// LiteLLMURL is LiteLLM's price file (MIT), the default price source
// (decisions §1).
const LiteLLMURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// The sync runs once a day; a failed one retries after an hour.
const (
	syncEvery = 24 * time.Hour
	syncRetry = time.Hour
)

func (s *Server) liteLLMURL() string { return cmp.Or(s.LiteLLMURL, LiteLLMURL) }

// tenantPairs are the (model, backend) pairs the tenant's backends serve,
// in backend order.
func (s *Server) tenantPairs(ctx context.Context, t string) ([][2]string, error) {
	bs, err := s.Store.Backends(ctx, t)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, b := range bs {
		for _, m := range b.Models {
			out = append(out, [2]string{m, b.Name})
		}
	}
	return out, nil
}

func (s *Server) pricing(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ctx := r.Context()
	pairs, err := s.tenantPairs(ctx, t)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.PriceRows(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := s.Store.PriceSources(ctx)
	if err != nil {
		return nil, err
	}
	seen, err := s.Store.LiteLLMSeen(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	v := pricingView(pairs, rows, keys, now)
	for i, p := range v.Prices {
		for r, x := range seen[[2]string{p.Model, p.Backend}] {
			if x != nil && p.LiteLLMKey != "" {
				if v.Prices[i].LiteLLM == nil {
					v.Prices[i].LiteLLM = map[string]float64{}
				}
				v.Prices[i].LiteLLM[pricing.Names[r]] = *x
			}
		}
	}
	props, err := s.Store.OpenProposals(ctx)
	if err != nil {
		return nil, err
	}
	served := map[[2]string]bool{}
	for _, p := range pairs {
		served[p] = true
	}
	for _, p := range props {
		if served[[2]string{p.ModelID, p.Backend}] {
			v.Proposals = append(v.Proposals, model.PriceProposal{ID: p.ID, Model: p.ModelID, Backend: p.Backend, Rate: p.Rate,
				Current: p.Current, Proposed: p.Proposed, LiteLLMKey: p.LiteLLMKey, CreatedAt: p.Created.UnixMilli()})
		}
	}
	last, lastOK, err := s.Store.LastSyncs(ctx)
	if err != nil {
		return nil, err
	}
	v.Sync = syncView(s.liteLLMURL(), last, lastOK, now)
	return v, nil
}

func syncView(url string, last, lastOK *store.SyncRun, now time.Time) model.PriceSync {
	v := model.PriceSync{Source: url, NextRunAt: now.UnixMilli()}
	if lastOK != nil {
		v.LastOKAt = lastOK.Finished.UnixMilli()
		v.NextRunAt = lastOK.Finished.Add(syncEvery).UnixMilli()
	}
	if last != nil {
		v.LastRunAt, v.Error = last.Finished.UnixMilli(), last.Error
		v.Applied, v.Proposed, v.Retired = last.Applied, last.Proposed, last.Retired
		if last.Error != "" {
			v.NextRunAt = min(v.NextRunAt, last.Finished.Add(syncRetry).UnixMilli())
			if lastOK == nil {
				v.NextRunAt = last.Finished.Add(syncRetry).UnixMilli()
			}
		}
	}
	return v
}

func inEffect(p store.PriceRow, now time.Time) bool {
	return !p.From.After(now) && (p.To == nil || p.To.After(now))
}

// pricingView takes rows sorted by model, backend, then effective_from. A
// change is a rate whose value or source differs from the pair's previous
// row, scheduled ones included; a row that ends with nothing after it ends
// every rate (no price). A pair's first row is its starting price, not a
// change.
func pricingView(pairs [][2]string, rows []store.PriceRow, keys map[[2]string]string, now time.Time) model.Pricing {
	out := model.Pricing{Prices: []model.PairPrice{}, Changes: []model.PriceChange{}, Proposals: []model.PriceProposal{}}
	byPair := map[[2]string][]store.PriceRow{}
	var order [][2]string
	for _, p := range rows {
		k := [2]string{p.ModelID, p.Backend}
		if byPair[k] == nil {
			order = append(order, k)
		}
		byPair[k] = append(byPair[k], p)
	}
	for _, k := range pairs {
		pp := model.PairPrice{Model: k[0], Backend: k[1], LiteLLMKey: keys[k], Rates: map[string]*model.PriceRate{},
			ETag: store.ETag(store.PriceVersion(store.PriceRow{ModelID: k[0], Backend: k[1]}))}
		if rs := byPair[k]; len(rs) > 0 {
			pp.ETag = store.ETag(store.PriceVersion(rs[len(rs)-1]))
			for _, p := range rs {
				if !inEffect(p, now) {
					continue
				}
				pp.EffectiveFrom, pp.Priced = p.From.UTC().Format(time.DateOnly), p.Rates.Priced()
				for i, v := range p.Rates {
					if v != nil {
						pp.Rates[pricing.Names[i]] = &model.PriceRate{PerM: *v, Source: string(p.Sources[i])}
					}
				}
			}
		}
		out.Prices = append(out.Prices, pp)
	}
	served := map[[2]string]bool{}
	for _, k := range pairs {
		served[k] = true
	}
	change := func(p store.PriceRow, i pricing.Rate, from, to *float64, src pricing.Source, at time.Time) model.PriceChange {
		return model.PriceChange{Model: p.ModelID, Backend: p.Backend, Field: pricing.Labels[i], From: from, To: to, Source: string(src),
			Effective: at.UTC().Format(time.DateOnly), EffectiveAt: at.UnixMilli(), Scheduled: at.After(now)}
	}
	ended := func(p store.PriceRow) {
		for i, v := range p.Rates {
			if v != nil {
				out.Changes = append(out.Changes, change(p, pricing.Rate(i), v, nil, "", *p.To))
			}
		}
	}
	for _, k := range order {
		if !served[k] {
			continue
		}
		rs := byPair[k]
		for j, p := range rs {
			if j == 0 {
				continue
			}
			prev := rs[j-1]
			if prev.To != nil && prev.To.Before(p.From) {
				ended(prev) // a gap: the price ended, then a new one started
				prev = store.PriceRow{}
			}
			for i := range pricing.NumRates {
				was, is := prev.Rates[i], p.Rates[i]
				if (was == nil) != (is == nil) || (was != nil && *was != *is) || prev.Sources[i] != p.Sources[i] {
					out.Changes = append(out.Changes, change(p, i, was, is, p.Sources[i], p.From))
				}
			}
		}
		if last := rs[len(rs)-1]; last.To != nil {
			ended(last)
		}
	}
	slices.SortStableFunc(out.Changes, func(x, y model.PriceChange) int { return cmp.Compare(y.EffectiveAt, x.EffectiveAt) })
	return out
}

// servedPair checks the path's (model, backend) is one the tenant's
// backends serve.
func (s *Server) servedPair(r *http.Request, t string) (string, string, error) {
	m, b := r.PathValue("model"), r.PathValue("backend")
	pairs, err := s.tenantPairs(r.Context(), t)
	if err != nil {
		return "", "", err
	}
	if !slices.Contains(pairs, [2]string{m, b}) {
		return "", "", store.ErrNotFound
	}
	return m, b, nil
}

// afterPriceWrite tells the gateway, then prices receipts that were waiting
// for this price.
func (s *Server) afterPriceWrite(ctx context.Context) {
	s.configChanged()
	if _, err := s.PriceLaterOnce(ctx); err != nil {
		log.Printf("price unpriced receipts: %v", err)
	}
}

// setPrice takes {rates: {input: 2.5, output: null, …}, effectiveFrom?}
// with If-Match: the pair's etag. A number overrides that rate; null puts
// it back on LiteLLM. Rates left out keep what they have. effectiveFrom is
// RFC 3339, now if omitted.
func (s *Server) setPrice(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	match, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	var in struct {
		Rates         map[string]*float64 `json:"rates"`
		EffectiveFrom string              `json:"effectiveFrom"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	m, b, err := s.servedPair(r, t)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	e := store.PriceEdit{ModelID: m, Backend: b, Set: map[pricing.Rate]*float64{}, From: now, IfMatch: match}
	for name, v := range in.Rates {
		rt, ok := store.RateByName(name)
		if !ok {
			return nil, badRequest(fmt.Sprintf("unknown rate %q: use input, cachedInput, cacheWrite, output or reasoning", name))
		}
		e.Set[rt] = v
	}
	if len(e.Set) == 0 {
		return nil, badRequest("send at least one rate")
	}
	if in.EffectiveFrom != "" {
		if e.From, err = time.Parse(time.RFC3339, in.EffectiveFrom); err != nil {
			return nil, badRequest("effectiveFrom must be an RFC 3339 time")
		}
	}
	if _, err := s.Store.SetPrice(r.Context(), t, actor(r), e, now); store.IsBadPrice(err) {
		return nil, badRequest(err.Error())
	} else if err != nil {
		return nil, err
	}
	s.afterPriceWrite(r.Context())
	return s.pricing(nil, r, t)
}

// cancelPrice drops a scheduled price change: DELETE
// /pricing/{model}/{backend}/{at}, with at its effectiveAt (epoch ms).
func (s *Server) cancelPrice(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ms, err := strconv.ParseInt(r.PathValue("at"), 10, 64)
	if err != nil {
		return nil, badRequest("at must be the change's effectiveAt (epoch ms)")
	}
	m, b, err := s.servedPair(r, t)
	if err != nil {
		return nil, err
	}
	err = s.Store.CancelPrice(r.Context(), t, actor(r), m, b, time.UnixMilli(ms), time.Now())
	if store.IsBadPrice(err) {
		return nil, badRequest(err.Error())
	} else if err != nil {
		return nil, err
	}
	s.afterPriceWrite(r.Context())
	return s.pricing(nil, r, t)
}

// setPriceSource takes {litellmKey}: the LiteLLM entry that prices the
// pair, "" for none. The key must be in LiteLLM's file; the sync runs at
// once so the price follows it.
func (s *Server) setPriceSource(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in struct {
		LiteLLMKey string `json:"litellmKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	m, b, err := s.servedPair(r, t)
	if err != nil {
		return nil, err
	}
	if in.LiteLLMKey != "" {
		file, err := s.fetchLiteLLM(r.Context())
		if err != nil {
			return nil, unavailable("couldn't load LiteLLM's price file to check the key: " + err.Error())
		}
		if _, ok := file[in.LiteLLMKey]; !ok {
			return nil, badRequest(fmt.Sprintf("LiteLLM's price file has no token prices under %q", in.LiteLLMKey))
		}
	}
	if err := s.Store.SetPriceSource(r.Context(), t, actor(r), m, b, in.LiteLLMKey); store.IsBadPrice(err) {
		return nil, badRequest(err.Error())
	} else if err != nil {
		return nil, err
	}
	if err := s.SyncPrices(r.Context()); err != nil {
		return nil, unavailable("the source is saved, but the sync failed: " + err.Error())
	}
	return s.pricing(nil, r, t)
}

// syncNow runs the LiteLLM sync: POST /pricing/sync.
func (s *Server) syncNow(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	if err := s.SyncPrices(r.Context()); err != nil {
		return nil, unavailable("LiteLLM sync failed: " + err.Error())
	}
	return s.pricing(nil, r, t)
}

func proposalID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, store.ErrNotFound
	}
	return id, nil
}

// acceptProposal puts the rate back on LiteLLM at the proposed value.
func (s *Server) acceptProposal(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	id, err := proposalID(r)
	if err != nil {
		return nil, err
	}
	if _, err := s.Store.AcceptProposal(r.Context(), t, actor(r), id, time.Now()); store.IsBadPrice(err) {
		return nil, badRequest(err.Error())
	} else if err != nil {
		return nil, err
	}
	s.afterPriceWrite(r.Context())
	return s.pricing(nil, r, t)
}

// dismissProposal keeps the override.
func (s *Server) dismissProposal(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	id, err := proposalID(r)
	if err != nil {
		return nil, err
	}
	if err := s.Store.DismissProposal(r.Context(), t, actor(r), id, time.Now()); err != nil {
		return nil, err
	}
	return s.pricing(nil, r, t)
}

// fetchLiteLLM loads and parses LiteLLM's price file.
func (s *Server) fetchLiteLLM(ctx context.Context) (map[string]pricing.Rates, error) {
	b, err := s.fetchLiteLLMFile(ctx)
	if err != nil {
		return nil, err
	}
	return pricing.ParseLiteLLM(b)
}

// liteLLMFresh is how long a fetched file is reused.
const liteLLMFresh = 5 * time.Minute

// fetchLiteLLMFile loads LiteLLM's price file, or the copy fetched in the
// last few minutes.
func (s *Server) fetchLiteLLMFile(ctx context.Context) ([]byte, error) {
	s.liteMu.Lock()
	defer s.liteMu.Unlock()
	if s.liteRaw != nil && time.Since(s.liteAt) < liteLLMFresh {
		return s.liteRaw, nil
	}
	b, err := s.downloadLiteLLM(ctx)
	if err == nil {
		s.liteRaw, s.liteAt = b, time.Now()
	}
	return b, err
}

func (s *Server) downloadLiteLLM(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.liteLLMURL(), nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d", s.liteLLMURL(), res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 64<<20))
}

// SyncPrices fetches LiteLLM's file and applies it (store.ApplySync). One
// sync runs at a time; a failure to load the file is recorded and returned.
func (s *Server) SyncPrices(ctx context.Context) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	started := time.Now()
	raw, err := s.fetchLiteLLMFile(ctx)
	var file map[string]pricing.Rates
	if err == nil {
		file, err = pricing.ParseLiteLLM(raw)
	}
	if err != nil {
		if rerr := s.Store.RecordSyncFailure(ctx, started, time.Now(), err); rerr != nil {
			log.Printf("record sync failure: %v", rerr)
		}
		return err
	}
	res, err := s.Store.ApplySync(ctx, file, started, time.Now())
	if err != nil {
		if rerr := s.Store.RecordSyncFailure(ctx, started, time.Now(), err); rerr != nil {
			log.Printf("record sync failure: %v", rerr)
		}
		return err
	}
	log.Printf("LiteLLM sync: %d applied, %d proposed, %d retired", res.Applied, res.Proposed, res.Retired)
	// Modalities and deprecation dates ride along; a failure here leaves the
	// last ones in place and doesn't fail the price sync.
	if facts, err := pricing.ParseFacts(raw); err != nil {
		log.Printf("LiteLLM facts: %v", err)
	} else if err := s.Store.SaveFacts(ctx, facts, time.Now()); err != nil {
		log.Printf("LiteLLM facts: %v", err)
	}
	if res.Applied+res.Retired > 0 {
		s.afterPriceWrite(ctx)
	}
	return nil
}

// RunPriceSync runs until ctx ends: the LiteLLM sync once a day (sooner
// after a failure), checked every 10 minutes.
func (s *Server) RunPriceSync(ctx context.Context) {
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		last, lastOK, err := s.Store.LastSyncs(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("price sync: %v", err)
		} else if due := syncView("", last, lastOK, time.Now()).NextRunAt; time.Now().UnixMilli() >= due {
			if err := s.SyncPrices(ctx); err != nil && ctx.Err() == nil {
				log.Printf("LiteLLM sync: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// PriceLaterOnce prices receipts that arrived without a price
// (store.PriceLater).
func (s *Server) PriceLaterOnce(ctx context.Context) (int, error) {
	prices, err := s.Store.PriceRows(ctx)
	if err != nil {
		return 0, err
	}
	models, err := s.Store.Models(ctx)
	if err != nil {
		return 0, err
	}
	byID := map[string]model.Model{}
	for _, m := range models {
		byID[m.ID] = m
	}
	n, err := s.Store.PriceUnpriced(ctx, prices, byID, time.Now())
	if n > 0 {
		log.Printf("priced %d receipts that arrived without a price", n)
	}
	return n, err
}

// PriceLater runs until ctx ends, pricing waiting receipts every minute
// (a scheduled price takes effect without a write to trigger it).
func (s *Server) PriceLater(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if _, err := s.PriceLaterOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("price unpriced receipts: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
