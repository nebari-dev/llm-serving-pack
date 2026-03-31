package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/models"
	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// UserGroupsLookup returns the current OIDC groups for a username.
// In production this calls the OIDC userinfo endpoint.
type UserGroupsLookup func(ctx context.Context, username string) ([]string, error)

// ModelLister provides model listing for the auditor.
type ModelLister interface {
	ListModels() []models.ModelInfo
}

// KeyManager provides key operations for the auditor.
type KeyManager interface {
	ListKeys(ctx context.Context, modelName string) ([]secrets.KeyInfo, error)
	RevokeKey(ctx context.Context, modelName, clientID string) error
}

// Auditor periodically checks API keys and revokes those whose creators
// no longer have group access to the model.
type Auditor struct {
	models   ModelLister
	keys     KeyManager
	lookup   UserGroupsLookup
	interval time.Duration
	logger   *slog.Logger
}

// NewAuditor creates a new Auditor.
func NewAuditor(models ModelLister, keys KeyManager, lookup UserGroupsLookup, interval time.Duration, logger *slog.Logger) *Auditor {
	return &Auditor{
		models:   models,
		keys:     keys,
		lookup:   lookup,
		interval: interval,
		logger:   logger,
	}
}

// Start runs the audit loop in a goroutine. Cancel the context to stop.
func (a *Auditor) Start(ctx context.Context) {
	go func() {
		if err := a.RunOnce(ctx); err != nil {
			a.logger.Error("audit pass failed", "error", err)
		}
		ticker := time.NewTicker(a.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := a.RunOnce(ctx); err != nil {
					a.logger.Error("audit pass failed", "error", err)
				}
			}
		}
	}()
}

// RunOnce performs a single audit pass. Exported for testing.
func (a *Auditor) RunOnce(ctx context.Context) error {
	allModels := a.models.ListModels()
	var errs []error

	for _, model := range allModels {
		// Public models are never audited for key revocation.
		if model.Public {
			continue
		}

		keys, err := a.keys.ListKeys(ctx, model.Name)
		if err != nil {
			a.logger.Error("failed to list keys for model", "model", model.Name, "error", err)
			errs = append(errs, fmt.Errorf("listing keys for model %s: %w", model.Name, err))
			continue
		}

		modelGroupSet := make(map[string]struct{}, len(model.Groups))
		for _, g := range model.Groups {
			modelGroupSet[g] = struct{}{}
		}

		for _, key := range keys {
			userGroups, err := a.lookup(ctx, key.Creator)
			if err != nil {
				// Fail-safe: if we cannot determine the user's groups, do not revoke.
				a.logger.Warn("failed to look up groups for user, skipping revocation",
					"user", key.Creator,
					"model", model.Name,
					"clientID", key.ClientID,
					"error", err,
				)
				continue
			}

			if hasOverlap(userGroups, modelGroupSet) {
				// User still has access; nothing to do.
				continue
			}

			// No overlapping groups - revoke the key.
			if err := a.keys.RevokeKey(ctx, model.Name, key.ClientID); err != nil {
				a.logger.Error("failed to revoke key",
					"user", key.Creator,
					"model", model.Name,
					"clientID", key.ClientID,
					"error", err,
				)
				errs = append(errs, fmt.Errorf("revoking key %s for model %s: %w", key.ClientID, model.Name, err))
				continue
			}

			a.logger.Info("revoked key due to lost group membership",
				"user", key.Creator,
				"model", model.Name,
				"clientID", key.ClientID,
			)
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return joinErrors(errs)
}

// hasOverlap returns true if any element in userGroups appears in modelGroupSet.
func hasOverlap(userGroups []string, modelGroupSet map[string]struct{}) bool {
	for _, g := range userGroups {
		if _, ok := modelGroupSet[g]; ok {
			return true
		}
	}
	return false
}

// joinErrors combines multiple errors into a single error.
func joinErrors(errs []error) error {
	return errors.Join(errs...)
}
