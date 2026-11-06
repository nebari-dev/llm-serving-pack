package openmeter

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/model"
)

// A long-open Traffic stream remembers only the receipts of its latest read,
// so its memory stays at one read's worth however much traffic passes.
func TestStreamForgetsReceiptsThatLeftTheWindow(t *testing.T) {
	var s streamed
	window := func(from int) []model.Receipt { // receiptScanCap receipts, newest first
		rs := make([]model.Receipt, receiptScanCap)
		for i := range rs {
			rs[i] = model.Receipt{ID: fmt.Sprintf("r%d", from+receiptScanCap-1-i)}
		}
		return rs
	}
	for from := 0; from < 50*receiptScanCap; from += receiptScanCap / 2 {
		s.update(window(from))
	}
	if n := len(s.sent); n > receiptScanCap {
		t.Fatalf("remembers %d receipts, want at most %d", n, receiptScanCap)
	}
	// Unchanged receipts still in the window are not sent again.
	last := window(50*receiptScanCap - receiptScanCap/2)
	if got := s.update(last); len(got) != 0 {
		t.Fatalf("resent %d unchanged receipts", len(got))
	}
}

// When OpenMeter fails the poller's first read, the first good read is the
// starting point: what was already there is not sent to open streams as
// new, and they aren't ready until then.
func TestStreamBaselineWaitsForAGoodRead(t *testing.T) {
	reads := make(chan struct{}, 8)
	calls := 0
	h := &hub{every: 10 * time.Millisecond, read: func(context.Context) ([]model.Receipt, int, error) {
		defer func() { reads <- struct{}{} }()
		calls++
		switch calls {
		case 1:
			return nil, 0, errors.New("openmeter unavailable")
		case 2:
			return []model.Receipt{{ID: "old"}}, 0, nil
		}
		return []model.Receipt{{ID: "new"}, {ID: "old"}}, 0, nil
	}}
	sub := h.subscribe(receiptFilter{})
	defer h.unsubscribe(sub)

	<-reads
	select {
	case <-sub.ready:
		t.Fatal("ready after a failed read")
	case <-time.After(5 * time.Millisecond):
	}
	select {
	case batch := <-sub.changes:
		if len(batch) != 1 || batch[0].id != "new" {
			t.Fatalf("sent %v, want only new", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing sent")
	}
}
