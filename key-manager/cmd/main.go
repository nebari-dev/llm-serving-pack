package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/api"
	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/audit"
	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/models"
	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/ui"
)

func main() {
	// 1. Parse config from env vars.
	apiKeysNamespace := getEnvOrDefault("LLM_API_KEYS_NAMESPACE", "llm-api-keys")
	groupsClaim := getEnvOrDefault("LLM_OIDC_GROUPS_CLAIM", "groups")
	cookiePrefix := getEnvOrDefault("LLM_AUTH_COOKIE_PREFIX", "IdToken")
	auditIntervalStr := getEnvOrDefault("LLM_AUDIT_INTERVAL", "5m")
	oidcUserinfoURL := os.Getenv("LLM_OIDC_USERINFO_URL") // optional
	listenAddr := getEnvOrDefault("LLM_LISTEN_ADDR", ":8080")
	devMode := strings.EqualFold(os.Getenv("LLM_DEV_MODE"), "true")
	devUser := getEnvOrDefault("LLM_DEV_USER", "dev")
	devGroups := splitAndTrim(getEnvOrDefault("LLM_DEV_GROUPS", "llm"))

	auditInterval, err := time.ParseDuration(auditIntervalStr)
	if err != nil {
		log.Fatalf("invalid LLM_AUDIT_INTERVAL %q: %v", auditIntervalStr, err)
	}

	logger := slog.Default()

	// 2. Set up K8s client with LLMModel scheme registered.
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Fatalf("adding client-go scheme: %v", err)
	}
	if err := llmv1alpha1.AddToScheme(scheme); err != nil {
		log.Fatalf("adding LLM scheme: %v", err)
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("getting in-cluster config: %v", err)
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("creating k8s client: %v", err)
	}

	// 3. Create components.
	watcher := models.NewWatcher(k8sClient)
	secretsMgr := secrets.NewManager(k8sClient, apiKeysNamespace)
	handler := api.NewHandler(watcher, secretsMgr, logger)

	// 4. Set up context with signal handling for graceful shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 5. Initial model sync.
	if err := watcher.Sync(ctx); err != nil {
		log.Printf("initial model sync failed (will retry): %v", err)
	}

	// 6. Start periodic model sync every 30s.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := watcher.Sync(ctx); err != nil {
					logger.Error("model sync failed", "error", err)
				}
			}
		}
	}()

	// 7. Start audit if userinfo URL is configured.
	if oidcUserinfoURL != "" {
		lookup := makeUserinfoLookup(oidcUserinfoURL)
		auditor := audit.NewAuditor(watcher, secretsMgr, lookup, auditInterval, logger)
		go auditor.Start(ctx)
		logger.Info("audit loop started", "interval", auditInterval, "userinfo_url", oidcUserinfoURL)
	} else {
		logger.Info("audit loop disabled: LLM_OIDC_USERINFO_URL not set")
	}

	// 8. Set up HTTP mux and routes.
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Serve static UI files at root.
	staticFS, err := fs.Sub(ui.StaticFiles, "static")
	if err != nil {
		log.Fatalf("creating sub-FS for static files: %v", err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(staticFS)))

	// Apply auth middleware only to /api/ routes.
	authConfig := api.AuthConfig{
		GroupsClaim:  groupsClaim,
		CookiePrefix: cookiePrefix,
		DevMode:      devMode,
	}
	if devMode {
		authConfig.DevIdentity = api.UserInfo{
			Username: devUser,
			Name:     devUser,
			Email:    devUser + "@local",
			Groups:   devGroups,
		}
		logger.Warn("LLM_DEV_MODE is enabled: auth is bypassed and a fixed identity is injected; never enable this in a real deployment",
			"username", devUser, "groups", devGroups)
	}
	authMW := api.AuthMiddleware(authConfig)

	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			authMW(mux).ServeHTTP(w, r)
		} else {
			mux.ServeHTTP(w, r)
		}
	})

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      finalHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Shut down the HTTP server when the context is cancelled.
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("http server shutdown error", "error", err)
		}
	}()

	log.Printf("key-manager listening on %s", listenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server error: %v", err)
	}
}

// getEnvOrDefault returns the value of the named environment variable, or
// the provided default if the variable is not set or is empty.
func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// splitAndTrim splits a comma-separated list, trimming whitespace and dropping
// empty entries. Used for LLM_DEV_GROUPS.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// makeUserinfoLookup returns a UserGroupsLookup that calls the OIDC userinfo endpoint.
// For v0.1 this is a stub that returns an error so the auditor skips revocation
// safely until token exchange is implemented.
func makeUserinfoLookup(userinfoURL string) audit.UserGroupsLookup {
	return func(ctx context.Context, username string) ([]string, error) {
		// TODO: Implement OIDC userinfo lookup.
		// This requires either:
		//   1. A service account token that can query the Keycloak admin API.
		//   2. Token exchange to obtain a token for the user.
		// Returning an error causes the auditor to skip revocation (fail-safe).
		return nil, fmt.Errorf("userinfo lookup not yet implemented (url: %s, user: %s)", userinfoURL, username)
	}
}
