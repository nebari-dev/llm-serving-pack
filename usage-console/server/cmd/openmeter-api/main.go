// Command openmeter-api serves the console read-only from OpenMeter: the
// console's API under /api/v1/<tenant>/, and the built console itself.
//
//	openmeter-api [flags]                      serve
//	openmeter-api bootstrap -config FILE.json  set up OpenMeter's features and prices, then exit
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/openmeter"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "bootstrap" {
		bootstrap(os.Args[2:])
		return
	}
	addr := flag.String("addr", ":8080", "listen address")
	omURL := flag.String("openmeter-url", cmp.Or(os.Getenv("OPENMETER_URL"), "http://localhost:8888"), "OpenMeter's API ($OPENMETER_URL); its token, if any, is $OPENMETER_TOKEN")
	consoleDir := flag.String("console-dir", "", "the built console (console/dist) to serve; empty serves the API only")
	flag.Parse()

	// The console calls its tenant "demo" (API_BASE in console/src/data/catalog.ts).
	s := &openmeter.Server{Client: openmeter.NewClient(*omURL, os.Getenv("OPENMETER_TOKEN")), Tenant: "demo", Now: time.Now}
	mux := http.NewServeMux()
	mux.Handle("/api/", s.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	if *consoleDir != "" {
		mux.Handle("/", console(*consoleDir))
	}

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	log.Printf("serving the console API from %s on %s", *omURL, *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// console serves the built console's files, and index.html for any other
// path (folders included, so none is listed), so the console's own routes
// (/spend, /traffic) load on refresh.
func console(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}

// bootstrap applies a BootstrapConfig, retrying while OpenMeter starts.
func bootstrap(args []string) {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	omURL := fs.String("openmeter-url", cmp.Or(os.Getenv("OPENMETER_URL"), "http://localhost:8888"), "OpenMeter's API")
	path := fs.String("config", "/etc/usage-console/bootstrap.json", "the features and prices to set up")
	timeout := fs.Duration("timeout", 5*time.Minute, "how long to keep trying")
	fs.Parse(args)
	b, err := os.ReadFile(*path)
	if err != nil {
		log.Fatal(err)
	}
	var cfg openmeter.BootstrapConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Fatalf("%s: %v", *path, err)
	}
	c := openmeter.NewClient(*omURL, os.Getenv("OPENMETER_TOKEN"))
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	for {
		err := openmeter.Bootstrap(ctx, c, cfg, time.Now)
		if err == nil {
			log.Printf("bootstrap: %d model resources, %d manual prices in place", len(cfg.Resources), len(cfg.Prices))
			return
		}
		log.Printf("bootstrap: %v; retrying", err)
		select {
		case <-ctx.Done():
			log.Fatalf("bootstrap: gave up after %s", *timeout)
		case <-time.After(5 * time.Second):
		}
	}
}
