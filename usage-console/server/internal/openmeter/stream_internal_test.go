package openmeter

import (
	"fmt"
	"testing"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/model"
)

// A long-open Traffic stream remembers only the receipts of its latest read,
// so its memory stays at one read's worth however much traffic passes.
func TestStreamForgetsReceiptsThatLeftTheWindow(t *testing.T) {
	var s streamed
	window := func(from int) []model.Receipt { // streamLimit receipts, newest first
		rs := make([]model.Receipt, streamLimit)
		for i := range rs {
			rs[i] = model.Receipt{ID: fmt.Sprintf("r%d", from+streamLimit-1-i)}
		}
		return rs
	}
	for from := 0; from < 50*streamLimit; from += streamLimit / 2 {
		s.update(window(from))
	}
	if n := len(s.sent); n > streamLimit {
		t.Fatalf("remembers %d receipts, want at most %d", n, streamLimit)
	}
	// Unchanged receipts still in the window are not sent again.
	last := window(50*streamLimit - streamLimit/2)
	if got := s.update(last); len(got) != 0 {
		t.Fatalf("resent %d unchanged receipts", len(got))
	}
}
