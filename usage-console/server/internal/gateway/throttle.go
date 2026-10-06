package gateway

import (
	"sync"
	"time"
)

// Throttle (spec §11 Phase 4: "throttle via rate-limit policy"): while a
// throttle budget covering a key is over its cap, the key may make
// ThrottleRate requests in any ThrottleWindow. Ten a minute keeps an
// interactive app usable (a person rarely sends more) while a batch job or a
// runaway agent, which is what overspends a budget, slows to a crawl. It's
// per key, so one busy key can't use up a whole team's allowance.
const (
	ThrottleRate   = 10
	ThrottleWindow = time.Minute
)

// Throttle counts each key's recent admitted requests. It lives in memory in
// the one process that decides (Warden, or devgateway), so with several
// Warden replicas each would allow the rate: a shared counter (Envoy's
// rate-limit service) would be needed then. The zero value is ready to use;
// a nil Throttle remembers nothing, so every request is a key's first.
type Throttle struct {
	mu     sync.Mutex
	recent map[string][]time.Time // admitted requests within the window, oldest first
}

func NewThrottle() *Throttle { return &Throttle{recent: map[string][]time.Time{}} }

// sweepAt is how many keys the throttle holds before it drops those with
// nothing left in the window.
const sweepAt = 1024

// Take admits a request for key at now if the key has made fewer than
// ThrottleRate in the window ending now, and records it; used is then how
// many the window holds, this one included. Otherwise wait is how long until
// the oldest leaves the window: the key's next open slot. A refused request
// doesn't take a slot.
func (t *Throttle) Take(key string, now time.Time) (ok bool, used int, wait time.Duration) {
	if t == nil {
		return true, 1, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.recent == nil {
		t.recent = map[string][]time.Time{}
	}
	if len(t.recent) >= sweepAt {
		for k, ts := range t.recent {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= ThrottleWindow {
				delete(t.recent, k)
			}
		}
	}
	ts := t.recent[key]
	for len(ts) > 0 && now.Sub(ts[0]) >= ThrottleWindow {
		ts = ts[1:]
	}
	if len(ts) >= ThrottleRate {
		t.recent[key] = ts
		return false, len(ts), ts[0].Add(ThrottleWindow).Sub(now)
	}
	ts = append(ts, now)
	t.recent[key] = ts
	return true, len(ts), 0
}
