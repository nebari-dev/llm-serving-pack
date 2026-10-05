package store

import (
	"errors"
	"testing"
)

// Rule order decides outcomes (the first block wins, the last reroute wins),
// so a reorder applies only to the order its author saw.
func TestPlanReorder(t *testing.T) {
	cur := []string{"r1", "r3", "r9"}
	names := map[string]string{"r1": "no-pii-out", "r3": "block-src", "r9": "eu-only"}

	moved, err := planReorder(cur, []string{"r1", "r3", "r9"}, []string{"r3", "r1", "r9"}, names)
	if err != nil || moved != "block-src 2 → 1, no-pii-out 1 → 2" {
		t.Fatalf("moved %q, err %v", moved, err)
	}
	if _, err := planReorder(cur, []string{"r3", "r1", "r9"}, []string{"r1", "r3", "r9"}, names); !errors.Is(err, ErrConflict) {
		t.Errorf("stale order: err %v, want conflict", err)
	}
	for _, bad := range [][]string{{"r3", "r1"}, {"r3", "r1", "r1"}, {"r3", "r1", "r7"}} {
		var e ErrBadOrder
		if _, err := planReorder(cur, cur, bad, names); !errors.As(err, &e) {
			t.Errorf("%v: err %v, want ErrBadOrder", bad, err)
		}
	}
	if _, err := planReorder(cur, cur, cur, names); !errors.Is(err, ErrSameOrder) {
		t.Errorf("no change: err %v", err)
	}
}
