package store

import (
	"testing"
	"time"
)

func TestDaySpanCoversOnlyWholeDays(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	for _, c := range []struct{ from, to, start, end string }{
		// 7d rolling: the partial first and last days come from receipts_5m.
		{"2026-09-18T14:30:00Z", "2026-09-25T14:30:00Z", "2026-09-19T00:00:00Z", "2026-09-25T00:00:00Z"},
		// Starting on midnight keeps that day.
		{"2026-09-01T00:00:00Z", "2026-09-25T14:30:00Z", "2026-09-01T00:00:00Z", "2026-09-25T00:00:00Z"},
		// Under a day: nothing from receipts_daily.
		{"2026-09-25T13:30:00Z", "2026-09-25T14:30:00Z", "2026-09-26T00:00:00Z", "2026-09-25T00:00:00Z"},
	} {
		start, end := DaySpan(at(c.from), at(c.to))
		if !start.Equal(at(c.start)) || !end.Equal(at(c.end)) {
			t.Errorf("DaySpan(%s, %s) = %s, %s; want %s, %s", c.from, c.to, start, end, c.start, c.end)
		}
	}
}
