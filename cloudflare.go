package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Discord's edge (Cloudflare) temp-bans an IP that returns more than ~10,000
// INVALID responses (401/403/429, not 404) in a rolling 10-minute window. That
// ban blocks everything, not just deletes, so we track those responses and pause
// all workers just short of the cap. We sit close to the limit deliberately
// (a big margin would needlessly throttle a legitimate run), with only enough
// headroom (pauseAt to cap) to absorb the handful of requests already in flight
// when we decide to stop.
const (
	cfWindow    = 10 * time.Minute
	cfHardLimit = 10000 // Discord's documented Cloudflare threshold
	cfPauseAt   = 9500  // stop launching new requests here
	cfResumeAt  = 9000  // resume once the window drains back below this (hysteresis)
)

// cfBudget is a rolling-window counter of invalid responses with a hard pause.
type cfBudget struct {
	mu    sync.Mutex
	times []int64 // unix-milli of invalid responses, ascending
	stats *Stats

	// tunable so tests can use small windows/thresholds
	window   time.Duration
	pauseAt  int
	resumeAt int
}

func newCFBudget(stats *Stats) *cfBudget {
	return &cfBudget{stats: stats, window: cfWindow, pauseAt: cfPauseAt, resumeAt: cfResumeAt}
}

// record notes one invalid (401/403/429) response.
func (b *cfBudget) record(now time.Time) {
	b.mu.Lock()
	b.times = append(b.times, now.UnixMilli())
	b.prune(now)
	n := len(b.times)
	b.mu.Unlock()
	b.stats.setInvalidWindow(n)
}

// prune drops entries older than the window. Caller holds the lock.
func (b *cfBudget) prune(now time.Time) {
	cutoff := now.Add(-b.window).UnixMilli()
	i := 0
	for i < len(b.times) && b.times[i] < cutoff {
		i++
	}
	if i > 0 {
		b.times = b.times[i:]
	}
}

// decide reports whether we're at/over the pause threshold and, if so, the time
// when enough entries will have aged out to fall below the resume threshold.
func (b *cfBudget) decide(now time.Time) (paused bool, wake time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune(now)
	n := len(b.times)
	if n < b.pauseAt {
		return false, time.Time{}
	}
	drop := n - b.resumeAt + 1 // how many must age out to get below resumeAt
	if drop < 1 {
		drop = 1
	}
	if drop > n {
		drop = n
	}
	return true, time.UnixMilli(b.times[drop-1]).Add(b.window)
}

func (b *cfBudget) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.times)
}

// waitIfExhausted blocks all callers while the invalid-response budget is spent,
// releasing once the window has drained enough. Returns false if ctx is done.
func (b *cfBudget) waitIfExhausted(ctx context.Context) bool {
	for {
		paused, wake := b.decide(time.Now())
		if !paused {
			return true
		}
		d := time.Until(wake)
		if d < time.Second {
			d = time.Second
		}
		b.stats.setStatus(fmt.Sprintf("Cloudflare safety: %d invalid responses in 10 min; holding just under the ~%d cap for %s",
			b.count(), cfHardLimit, d.Round(time.Second)))
		if !sleepCtx(ctx, d) {
			return false
		}
	}
}
