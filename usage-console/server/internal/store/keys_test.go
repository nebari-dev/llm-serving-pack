package store

import (
	"testing"
	"time"
)

func TestKeyHourBinsTheRollingDay(t *testing.T) {
	from := time.Date(2026, 9, 27, 14, 35, 0, 0, time.UTC) // now − 24h, on a 5-minute bucket
	for _, c := range []struct {
		bucket time.Duration
		want   int
	}{
		{5 * time.Minute, 0},
		{55 * time.Minute, 0},
		{time.Hour, 1},
		{23*time.Hour + 55*time.Minute, 23},
		// The bucket holding now lands in the last hour, not past it.
		{24 * time.Hour, 23},
		// Before the window: clamp rather than index out of range.
		{-5 * time.Minute, 0},
	} {
		if got := keyHour(from, from.Add(c.bucket)); got != c.want {
			t.Errorf("keyHour(from+%s) = %d, want %d", c.bucket, got, c.want)
		}
	}
}

func TestKeyUsageAddKeepsTotalsAndBins(t *testing.T) {
	from := time.Date(2026, 9, 27, 14, 35, 0, 0, time.UTC)
	var u KeyUsage
	u.add(from, from.Add(10*time.Minute), 3, 0.5)
	u.add(from, from.Add(20*time.Minute), 2, 0.25)
	u.add(from, from.Add(23*time.Hour+30*time.Minute), 4, 1)
	if u.Requests24h != 9 || u.Spend24hUSD != 1.75 {
		t.Fatalf("totals %d / %v, want 9 / 1.75", u.Requests24h, u.Spend24hUSD)
	}
	if u.Hourly[0] != 5 || u.Hourly[23] != 4 {
		t.Fatalf("hourly %v", u.Hourly)
	}
	sum := 0
	for _, n := range u.Hourly {
		sum += n
	}
	if sum != u.Requests24h {
		t.Fatalf("hourly sums to %d, not requests24h %d", sum, u.Requests24h)
	}
}
