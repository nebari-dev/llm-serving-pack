package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/identity"
	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/openmeter"
	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// startCustomerSync keeps OpenMeter customers in step with API keys when
// LLM_OPENMETER_URL is set, so usage made with a key is attributed to the
// person who minted it (see package openmeter). With the variable unset it
// does nothing.
func startCustomerSync(ctx context.Context, keys *secrets.Manager, logger *slog.Logger) {
	baseURL := os.Getenv("LLM_OPENMETER_URL")
	if baseURL == "" {
		logger.Info("openmeter customer sync disabled: LLM_OPENMETER_URL not set")
		return
	}
	interval, err := time.ParseDuration(getEnvOrDefault("LLM_OPENMETER_SYNC_INTERVAL", "30s"))
	if err != nil {
		logger.Error("invalid LLM_OPENMETER_SYNC_INTERVAL; using 30s", "error", err)
		interval = 30 * time.Second
	}
	client := openmeter.NewClient(baseURL, os.Getenv("LLM_OPENMETER_TOKEN"), &http.Client{Timeout: 10 * time.Second})
	sync := openmeter.NewCustomerSync(keys, client, logger)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := sync.Sync(ctx); err != nil {
				logger.Error("openmeter customer sync failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	logger.Info("openmeter customer sync enabled", "url", baseURL, "interval", interval)
}

// startIdentityLookup serves the API-key owner lookup (package identity) on
// LLM_INTERNAL_LISTEN_ADDR when it is set. It has no authentication, so it
// listens apart from the public key API and is reached only inside the
// cluster.
func startIdentityLookup(ctx context.Context, keys *secrets.Manager, logger *slog.Logger) {
	addr := os.Getenv("LLM_INTERNAL_LISTEN_ADDR")
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	identity.NewHandler(keys).RegisterRoutes(mux)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("identity lookup server stopped", "error", err)
		}
	}()
	go func() { <-ctx.Done(); _ = srv.Close() }()
	logger.Info("identity lookup enabled", "addr", addr)
}
