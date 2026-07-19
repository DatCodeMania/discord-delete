package main

import (
	"context"
	"sync"
)

// When Discord throttles a delete with a shared/global (account-wide) scope,
// extra workers only generate redundant 429s without adding throughput: the
// limit is one bucket shared by the whole account (this is what deleting old,
// >~2-week messages hits). Once that's clearly happening we collapse to a
// single active worker for the rest of the run. It's one-way (sticky) on
// purpose: recovering would just re-trigger the 429 storm and oscillate. A
// fresh run re-evaluates from full concurrency.
const (
	throttlePenalty   = 2 // score added per account-wide 429
	collapseThreshold = 4 // score at/above which concurrency drops to 1 (i.e. 2 such 429s)
)

// concurrencyController is a dynamic semaphore bounding how many workers may have
// a delete in flight at once, plus the throttle scoring that collapses it to 1.
type concurrencyController struct {
	mu     sync.Mutex
	cond   *sync.Cond
	full   int
	limit  int
	active int
	score  int
	stats  *Stats
}

func newConcurrencyController(full int, stats *Stats) *concurrencyController {
	if full < 1 {
		full = 1
	}
	c := &concurrencyController{full: full, limit: full, stats: stats}
	c.cond = sync.NewCond(&c.mu)
	stats.setActiveLimit(full)
	return c
}

// acquire blocks until a slot is free, calling onWait once if it actually has to
// wait (so the caller can mark that worker idle). Returns false if ctx is done.
func (c *concurrencyController) acquire(ctx context.Context, onWait func()) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	waited := false
	for c.active >= c.limit {
		if ctx.Err() != nil {
			return false
		}
		if !waited && onWait != nil {
			onWait()
			waited = true
		}
		c.cond.Wait()
	}
	if ctx.Err() != nil {
		return false
	}
	c.active++
	return true
}

func (c *concurrencyController) release() {
	c.mu.Lock()
	if c.active > 0 {
		c.active--
	}
	c.cond.Signal()
	c.mu.Unlock()
}

// wakeAll unblocks every waiter so parked workers can re-check ctx and exit.
// Spawn a goroutine that calls this on ctx.Done(), since sync.Cond isn't
// context-aware.
func (c *concurrencyController) wakeAll() {
	c.mu.Lock()
	c.cond.Broadcast()
	c.mu.Unlock()
}

// sharedThrottle records an account-wide (shared/global) 429. Once enough have
// accrued it collapses to a single worker (permanently for this run).
func (c *concurrencyController) sharedThrottle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.limit <= 1 {
		return // already collapsed (or only ever had one worker)
	}
	c.score += throttlePenalty
	if c.score >= collapseThreshold {
		c.limit = 1
		c.stats.setActiveLimit(1)
		c.stats.setStatus("account-wide rate limit: reduced to 1 worker (extra workers can't delete these any faster)")
		c.cond.Broadcast()
	}
}
