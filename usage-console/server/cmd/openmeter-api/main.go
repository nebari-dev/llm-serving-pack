// Command openmeter-api serves the console read-only from OpenMeter: the
// console's API under /api/v1/<tenant>/, and the built console itself.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nebari-dev/openmeter-pack/ui/server/internal/demo"
	"github.com/nebari-dev/openmeter-pack/ui/server/internal/openmeter"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	omURL := flag.String("openmeter-url", cmp.Or(os.Getenv("OPENMETER_URL"), "http://localhost:8888"), "OpenMeter's API ($OPENMETER_URL); its token, if any, is $OPENMETER_TOKEN")
	consoleDir := flag.String("console-dir", "", "the built console (console/dist) to serve; empty serves the API only")
	flag.Parse()

	s := &openmeter.Server{Client: openmeter.NewClient(*omURL, os.Getenv("OPENMETER_TOKEN")), Tenant: demo.Tenant, Now: time.Now}
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
