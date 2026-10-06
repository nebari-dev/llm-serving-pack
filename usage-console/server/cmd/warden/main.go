// warden is Agent Router's policy ext_proc (spec §4.5, request path): budgets,
// rules, redaction and reroutes, from the same engine as devgateway. It serves
// Envoy's ext_proc gRPC API on :8083 and a small admin API on :8084:
//
//	GET  /healthz                  snapshot age, kill-switch state, version
//	GET  /metrics                  warden_snapshot_age_seconds, warden_passthrough
//	POST /passthrough?on=true|false  the kill switch (§9.3), no restart needed
//
// Config is a snapshot reloaded in the background; requests never wait on the
// database. If the database goes away, Warden keeps serving the last snapshot.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jbouder/stargate/server/internal/buildinfo"
	"github.com/jbouder/stargate/server/internal/config"
	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/store"
	"github.com/jbouder/stargate/server/internal/warden"
)

func main() {
	addr := flag.String("addr", ":8083", "ext_proc gRPC listen address")
	admin := flag.String("admin", ":8084", "admin HTTP listen address")
	refresh := flag.Duration("refresh", 5*time.Second, "how often to reload config from the control plane db")
	deadline := flag.Duration("deadline", 50*time.Millisecond, "per-request evaluation deadline; past it the rules' fail mode applies")
	passthrough := flag.Bool("passthrough", false, "start with the kill switch on: requests pass unpoliced")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := store.Open(ctx, config.ConfigDB, config.ReceiptsDB)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	var cur gateway.Current
	var loadedAt atomic.Int64
	// Loads are serialized so a slow tick can't overwrite a newer /reload.
	var loadMu sync.Mutex
	load := func() error {
		loadMu.Lock()
		defer loadMu.Unlock()
		s, err := gateway.LoadSnapshot(ctx, st, demo.Tenant)
		if err == nil {
			cur.Store(s)
			loadedAt.Store(time.Now().UnixNano())
		}
		return err
	}
	if err := load(); err != nil {
		log.Fatalf("load config (run `stargate-api migrate` first?): %v", err)
	}
	early := make(chan struct{}, 1)
	go func() {
		t := time.NewTicker(*refresh)
		defer t.Stop()
		var last time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-early:
				if time.Since(last) < time.Second {
					continue
				}
			case <-t.C:
			}
			last = time.Now()
			if err := load(); err != nil && ctx.Err() == nil {
				log.Printf("reload config (serving the cached snapshot): %v", err)
			}
		}
	}()

	w := &warden.Server{Snap: &cur, Deadline: *deadline, Refresh: func() {
		select {
		case early <- struct{}{}:
		default:
		}
	}}
	w.SetPassthrough(*passthrough)
	// Content from routes that capture (§9.2) is written off the request path.
	contents := st.NewContentWriter(ctx, 1024)
	w.Capture = contents.Put

	age := func() float64 { return time.Since(time.Unix(0, loadedAt.Load())).Seconds() }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(map[string]any{"snapshotAgeSeconds": age(), "passthrough": w.Passthrough(), "deadlineMs": deadline.Milliseconds(), "captureDropped": contents.Dropped.Load(), "version": buildinfo.Get()})
	})
	mux.HandleFunc("GET /metrics", func(rw http.ResponseWriter, _ *http.Request) {
		pt := 0
		if w.Passthrough() {
			pt = 1
		}
		fmt.Fprintf(rw, "# TYPE warden_snapshot_age_seconds gauge\nwarden_snapshot_age_seconds %.3f\n# TYPE warden_passthrough gauge\nwarden_passthrough %d\n", age(), pt)
	})
	// The control plane calls this after a config write, so a new cap or rule
	// applies at once instead of on the next tick. It answers once loaded.
	mux.HandleFunc("POST /reload", func(rw http.ResponseWriter, _ *http.Request) {
		if err := load(); err != nil {
			http.Error(rw, err.Error(), http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(rw, "reloaded")
	})
	mux.HandleFunc("POST /passthrough", func(rw http.ResponseWriter, r *http.Request) {
		on, err := strconv.ParseBool(r.URL.Query().Get("on"))
		if err != nil {
			http.Error(rw, "want ?on=true or ?on=false", http.StatusBadRequest)
			return
		}
		w.SetPassthrough(on)
		log.Printf("kill switch: passthrough=%v", on)
		fmt.Fprintf(rw, "passthrough=%v\n", on)
	})
	hs := &http.Server{Addr: *admin, Handler: mux}
	go func() {
		if err := hs.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	// Panics during evaluation fail to the rules' fail mode inside Warden;
	// this catches any elsewhere, so one request can't take the process down.
	gs := grpc.NewServer(grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("warden: panic in %s: %v", info.FullMethod, r)
				err = status.Errorf(codes.Internal, "warden panic: %v", r)
			}
		}()
		return h(srv, ss)
	}))
	extprocv3.RegisterExternalProcessorServer(gs, w)
	go func() {
		<-ctx.Done()
		hs.Close()
		gs.GracefulStop()
	}()
	log.Printf("warden listening on %s (ext_proc), admin on %s, deadline %v, passthrough=%v", *addr, *admin, *deadline, *passthrough)
	if err := gs.Serve(lis); err != nil {
		log.Fatal(err)
	}
}
