// Command openmeter-api serves the console read-only from OpenMeter: the
// console's API under /api/v1/<tenant>/, and the built console itself. The
// openmeter subchart's setup Jobs run openmeter-setup (openmeter-setup/), not
// this command.
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
	"strings"
	"syscall"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/shared/oidc"
	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/openmeter"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	omURL := flag.String("openmeter-url", cmp.Or(os.Getenv("OPENMETER_URL"), "http://localhost:8888"), "OpenMeter's API ($OPENMETER_URL); its token, if any, is $OPENMETER_TOKEN")
	consoleDir := flag.String("console-dir", "", "the built console (console/dist) to serve; empty serves the API only")
	issuer := flag.String("oidc-issuer-url", os.Getenv("OIDC_ISSUER_URL"), "the issuer of the access tokens the gateway forwards ($OIDC_ISSUER_URL); required unless -dev")
	discovery := flag.String("oidc-discovery-url", os.Getenv("OIDC_DISCOVERY_URL"), "where to read the issuer's discovery document and keys, if not the issuer URL ($OIDC_DISCOVERY_URL)")
	clientID := flag.String("oidc-client-id", os.Getenv("OIDC_CLIENT_ID"), "the client the tokens must be issued to ($OIDC_CLIENT_ID); required with -oidc-issuer-url")
	groups := flag.String("oidc-groups", os.Getenv("OIDC_GROUPS"), "comma-separated Keycloak groups whose members may use the console ($OIDC_GROUPS); empty: every signed-in person")
	dev := flag.Bool("dev", os.Getenv("CONSOLE_DEV_MODE") == "true", "no sign-in: every caller is the same viewer ($CONSOLE_DEV_MODE=true); for a dev cluster only")
	flag.Parse()

	identity, stopIdentity, err := newIdentity(*issuer, *discovery, *clientID, *groups, *dev)
	if err != nil {
		log.Fatal(err)
	}
	defer stopIdentity()

	// The console calls its tenant "demo" (API_BASE in console/src/data/catalog.ts).
	s := &openmeter.Server{Client: openmeter.NewClient(*omURL, os.Getenv("OPENMETER_TOKEN")), Tenant: "demo", Now: time.Now, Identity: identity}
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

// newIdentity is how the console checks who is calling. The console shows
// everyone's usage, so it refuses to start without sign-in unless dev is set,
// and refuses an issuer without a client ID (any client in the realm would do).
func newIdentity(issuer, discovery, clientID, groups string, dev bool) (openmeter.Identity, func(), error) {
	switch {
	case dev && issuer != "":
		return nil, nil, errors.New("set either -dev ($CONSOLE_DEV_MODE) or -oidc-issuer-url ($OIDC_ISSUER_URL), not both")
	case dev:
		log.Printf("sign-in: none (dev mode): every caller is the same viewer; never use this in a real deployment")
		return openmeter.DevIdentity{}, func() {}, nil
	case issuer == "":
		return nil, nil, errors.New("-oidc-issuer-url ($OIDC_ISSUER_URL) is required unless -dev ($CONSOLE_DEV_MODE=true): the console shows everyone's usage")
	case clientID == "":
		return nil, nil, errors.New("-oidc-client-id ($OIDC_CLIENT_ID) is required with -oidc-issuer-url")
	}
	keys := discovery
	if keys == "" {
		keys = issuer
	}
	v := oidc.New(oidc.Config{DiscoveryURL: keys, Issuer: issuer, ClientID: clientID, AcceptAudience: true})
	var required []string
	for _, g := range strings.Split(groups, ",") {
		if g = strings.TrimSpace(g); g != "" {
			required = append(required, g)
		}
	}
	log.Printf("sign-in: tokens from %s issued to %s, groups %v", issuer, clientID, required)
	return openmeter.GatewayIdentity{Tokens: v, Groups: required}, v.Stop, nil
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
