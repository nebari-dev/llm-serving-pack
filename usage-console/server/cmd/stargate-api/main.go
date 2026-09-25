// stargate-api is the control plane: REST + SSE for the console, and the
// external authorization service Agent Router checks keys against.
//
//	stargate-api serve     migrate, seed the demo tenant if missing, serve
//	stargate-api migrate   apply migrations only
//	stargate-api backfill  synthesize history into the receipts db
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/jbouder/stargate/server/internal/api"
	"github.com/jbouder/stargate/server/internal/config"
	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
	"github.com/jbouder/stargate/server/internal/traffic"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: stargate-api serve|migrate|backfill [flags]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := store.Open(ctx, config.ConfigDB, config.ReceiptsDB)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if seeded, err := st.Seed(ctx); err != nil {
		log.Fatalf("seed: %v", err)
	} else if seeded {
		log.Printf("seeded demo tenant")
	}

	switch os.Args[1] {
	case "migrate":
		log.Print("migrations applied")
	case "serve":
		serve(ctx, st, os.Args[2:])
	case "backfill":
		backfill(ctx, st, os.Args[2:])
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func serve(ctx context.Context, st *store.Store, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	authzAddr := fs.String("authz-addr", ":8082", "listen address for Agent Router's ext_authz checks")
	refresh := fs.Duration("refresh", 5*time.Second, "how often ext_authz reloads keys from the db")
	warden := fs.String("warden", "", "Warden's admin URL (e.g. http://localhost:8084), for the console's degradation banner; empty when Warden isn't in the path")
	fs.Parse(args)

	// Loads are serialized so a slow periodic one can't overwrite a newer one.
	var snap gateway.Current
	var mu sync.Mutex
	reload := func() {
		mu.Lock()
		defer mu.Unlock()
		s, err := gateway.LoadSnapshot(ctx, st, demo.Tenant)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("load gateway config: %v", err)
			}
			return
		}
		snap.Store(s)
	}
	if reload(); snap.Load() == nil {
		log.Fatal("no gateway config to check keys against")
	}
	go func() {
		t := time.NewTicker(*refresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				reload()
			}
		}
	}()

	hub := api.NewHub()
	go hub.Listen(ctx, st, st.Receipts)
	// Reloading before the key mutation responds means a revoked key is
	// refused from the moment the console shows it revoked.
	srv := &api.Server{Store: st, Hub: hub, Tenants: []string{demo.Tenant}, DevActor: "dev@localhost", KeysChanged: reload, WardenURL: *warden}
	go srv.FinishRotations(ctx)

	go listen(ctx, "ext_authz", *authzAddr, &gateway.ExtAuthz{Snap: &snap})
	listen(ctx, "stargate-api", *addr, logRequests(srv.Handler()))
}

func listen(ctx context.Context, name, addr string, h http.Handler) {
	hs := &http.Server{Addr: addr, Handler: h}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	log.Printf("%s listening on %s", name, addr)
	if err := hs.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		h.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(t).Round(time.Millisecond))
		}
	})
}

// backfill runs generated requests through the real gateway engine against the
// in-process fake upstream, stamped across past days with a daily rhythm, so
// charts and budgets have history before live traffic accumulates.
func backfill(ctx context.Context, st *store.Store, args []string) {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	days := fs.Int("days", 7, "days of history to synthesize")
	perDay := fs.Int("per-day", 3000, "average requests per day")
	seed := fs.Uint64("seed", 20260924, "random seed")
	fs.Parse(args)

	snap, err := gateway.LoadSnapshot(ctx, st, demo.Tenant)
	if err != nil {
		log.Fatal(err)
	}
	snap.Spend = store.MonthSpend{} // replaying history: don't let today's spend block the past
	r := rand.New(rand.NewPCG(*seed, *seed^0x9e3779b97f4a7c15))
	gen := traffic.New()
	up := &gateway.SimUpstream{Rand: r}

	end := time.Now().Add(-time.Minute)
	start := end.Add(-time.Duration(*days) * 24 * time.Hour)
	total := *days * *perDay
	batch := make([]*model.Receipt, 0, 2000)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := st.CopyReceipts(ctx, batch); err != nil {
			log.Fatalf("copy: %v", err)
		}
		batch = batch[:0]
	}
	for i := 0; i < total; i++ {
		ts := diurnal(r, start, end)
		req := gen.Next(r)
		in := gateway.Input{Secret: req.Secret, Region: req.Region, SessionID: req.SessionID, Actor: req.Actor, Req: req.Body, Body: []byte(req.Body.Messages[1].Content), Now: ts}
		d := gateway.Admit(snap, in, r)
		var rc *model.Receipt
		if d.Reject != nil {
			rc = d.Finish(snap, nil, gateway.Result{}, nil, ts.Add(time.Duration(18+r.IntN(30))*time.Millisecond))
		} else {
			c, res, failed := gateway.Execute(ctx, d, up, func(string) bool { return true })
			rc = d.Finish(snap, c, res, failed, ts.Add(res.Duration))
		}
		if rc != nil {
			batch = append(batch, rc)
		}
		if len(batch) == cap(batch) {
			flush()
			log.Printf("backfill %d/%d", i+1, total)
		}
	}
	flush()
	if err := st.RefreshAggregates(ctx, start.Add(-24*time.Hour), time.Now()); err != nil {
		log.Fatalf("refresh aggregates: %v", err)
	}
	log.Printf("backfilled %d receipts over %d days", total, *days)
}

// diurnal samples a time in [start, end) weighted toward working hours (UTC),
// by rejection against the same curve the mockup's traffic chart used.
func diurnal(r *rand.Rand, start, end time.Time) time.Time {
	span := end.Sub(start)
	for {
		t := start.Add(time.Duration(r.Int64N(int64(span))))
		h := float64(t.UTC().Hour()) + float64(t.UTC().Minute())/60
		w := 0.55 + 0.45*math.Sin((h-6)/24*2*math.Pi)
		if r.Float64() < w {
			return t
		}
	}
}
