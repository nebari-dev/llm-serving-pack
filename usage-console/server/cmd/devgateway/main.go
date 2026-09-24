// devgateway is an OpenAI-compatible endpoint standing in for Envoy AI
// Gateway + Warden until the full spine lands. It enforces the control plane's
// config and writes a receipt for every request.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/jbouder/stargate/server/internal/config"
	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/store"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	upstream := flag.String("upstream", "http://localhost:8090", "OpenAI-compatible upstream base URL; requests go to {upstream}/{backend}/v1/chat/completions")
	refresh := flag.Duration("refresh", 5*time.Second, "how often to reload config from the control plane db")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := store.Open(ctx, config.ConfigDB, config.ReceiptsDB)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	var cur gateway.Current
	load := func() error {
		s, err := gateway.LoadSnapshot(ctx, st, demo.Tenant)
		if err == nil {
			cur.Store(s)
		}
		return err
	}
	if err := load(); err != nil {
		log.Fatalf("load config (run `stargate-api migrate` first?): %v", err)
	}
	go func() {
		t := time.NewTicker(*refresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := load(); err != nil && ctx.Err() == nil {
					log.Printf("reload config: %v", err)
				}
			}
		}
	}()

	g := &gateway.Server{Snap: &cur, Store: st, Up: &gateway.HTTPUpstream{BaseURL: *upstream, Client: &http.Client{Timeout: 2 * time.Minute}}}
	hs := &http.Server{Addr: *addr, Handler: g.Handler()}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	log.Printf("devgateway listening on %s → %s", *addr, *upstream)
	if err := hs.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
