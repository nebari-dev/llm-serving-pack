package api

import (
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

func TestWithUsageCarriesSpendAndHourly(t *testing.T) {
	u := store.KeyUsage{Requests24h: 7, Spend24hUSD: 1.5}
	u.Hourly[23] = 7
	k := withUsage(model.APIKey{ID: "k1", Status: "active"}, u)
	if k.Requests24h != 7 || k.Spend24hUSD != 1.5 || len(k.Hourly24h) != 24 || k.Hourly24h[23] != 7 {
		t.Fatalf("active key usage %+v", k)
	}
	r := withUsage(model.APIKey{ID: "k1", Status: "revoked"}, u)
	if r.Requests24h != 0 || r.Spend24hUSD != 0 || len(r.Hourly24h) != 24 || r.Hourly24h[23] != 0 {
		t.Fatalf("a revoked key should report no usage: %+v", r)
	}
}

func TestRotationFromRotateUntilAndAudit(t *testing.T) {
	ends := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	started := store.AuditMark{TS: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC), Actor: "dev@stargate.local"}
	rotating := store.KeyRecord{APIKey: model.APIKey{ID: "k4", Status: "rotating"}, RotateUntil: &ends}

	r := rotationOf(rotating, map[string]store.AuditMark{"k4": started})
	if r == nil || r.EndsAt == nil || *r.EndsAt != ends.UnixMilli() {
		t.Fatalf("endsAt from rotate_until: %+v", r)
	}
	if r.StartedAt == nil || *r.StartedAt != started.TS.UnixMilli() || r.StartedBy == nil || *r.StartedBy != started.Actor {
		t.Fatalf("start from the audit log: %+v", r)
	}

	// A rotation with no audit row or no end says so rather than guessing.
	bare := rotationOf(store.KeyRecord{APIKey: model.APIKey{ID: "k4", Status: "rotating"}}, nil)
	if bare == nil || bare.EndsAt != nil || bare.StartedAt != nil || bare.StartedBy != nil {
		t.Fatalf("unrecorded rotation: %+v", bare)
	}

	if rotationOf(store.KeyRecord{APIKey: model.APIKey{ID: "k1", Status: "active"}, RotateUntil: &ends}, map[string]store.AuditMark{"k1": started}) != nil {
		t.Fatal("only rotating keys carry a rotation")
	}
}
