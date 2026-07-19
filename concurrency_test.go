package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// A single shared-scope 429 shouldn't collapse (fluke protection); two should.
func TestControllerCollapsesOnSustainedSharedThrottle(t *testing.T) {
	stats := NewStats(0, 4)
	c := newConcurrencyController(4, stats)
	if got := stats.Snapshot().ActiveLimit; got != 4 {
		t.Fatalf("expected to start at full concurrency 4, got %d", got)
	}

	c.sharedThrottle() // one account-wide 429, not enough on its own
	if got := stats.Snapshot().ActiveLimit; got != 4 {
		t.Fatalf("one shared 429 should not collapse, got %d", got)
	}

	c.sharedThrottle() // second reaches the threshold
	if got := stats.Snapshot().ActiveLimit; got != 1 {
		t.Fatalf("sustained shared throttling should collapse to 1, got %d", got)
	}

	// Sticky: further throttling stays at 1, never oscillates back up.
	c.sharedThrottle()
	if got := stats.Snapshot().ActiveLimit; got != 1 {
		t.Fatalf("collapse must be sticky, got %d", got)
	}
}

// Once collapsed to 1, acquire lets exactly one worker in; others block until it
// releases. Also verifies acquire unblocks (returns false) when ctx is cancelled.
func TestControllerAcquireRespectsCollapsedLimit(t *testing.T) {
	stats := NewStats(0, 3)
	c := newConcurrencyController(3, stats)
	c.sharedThrottle()
	c.sharedThrottle() // collapse to 1

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !c.acquire(ctx, nil) {
		t.Fatal("first acquire should succeed with a free slot")
	}

	got := make(chan bool, 1)
	go func() { got <- c.acquire(ctx, nil) }()
	select {
	case <-got:
		t.Fatal("second acquire should block while the single slot is held")
	case <-time.After(80 * time.Millisecond):
	}

	// Cancelling the context should release the parked acquire (returns false).
	go func() { <-ctx.Done(); c.wakeAll() }()
	cancel()
	select {
	case ok := <-got:
		if ok {
			t.Fatal("acquire should return false when ctx is cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled acquire did not unblock")
	}
}

// End-to-end: a server returning shared-scope 429s drives the engine down to a
// single active worker.
func TestEngineCollapsesOnSharedScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Scope", "shared") // account-wide old-message limit
		w.WriteHeader(429)
		w.Write([]byte(`{"global":false,"retry_after":0.005}`))
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	const total = 6
	ids := make([]string, total)
	for i := range ids {
		ids[i] = strconv.Itoa(2000000 + i)
	}
	stats := NewStats(total, 4)
	eng := NewEngine(EngineConfig{Workers: 4, DryRun: false, GlobalMinInterval: time.Millisecond}, stats)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	eng.Run(ctx, []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: ids}})

	if got := stats.Snapshot().ActiveLimit; got != 1 {
		t.Fatalf("shared-scope throttling should collapse the engine to 1 worker, got %d", got)
	}
}

// After collapsing to 1 worker mid-run, every message in every channel must
// still get deleted: parked workers' channels drain through the single shared
// slot instead of being stranded or requeued to one worker.
func TestCollapseStillDrainsAllChannels(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First few requests are account-wide 429s (enough to collapse to 1),
		// then everything succeeds, so we can confirm nothing was left behind.
		if n.Add(1) <= 4 {
			w.Header().Set("X-RateLimit-Scope", "shared")
			w.WriteHeader(429)
			w.Write([]byte(`{"global":false,"retry_after":0.005}`))
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	// 3 channels of 4 messages each with 4 workers: 3 workers each start a
	// channel, so the collapse proves nobody's stranded only if all 3 finish.
	var jobs []ChannelJob
	const perCh = 4
	total := 0
	for c := 0; c < 3; c++ {
		ids := make([]string, perCh)
		for i := range ids {
			ids[i] = strconv.Itoa(4000000 + c*100 + i)
		}
		jobs = append(jobs, ChannelJob{ChannelID: strconv.Itoa(c), Label: "ch" + strconv.Itoa(c), MsgIDs: ids})
		total += perCh
	}

	stats := NewStats(total, 4)
	eng := NewEngine(EngineConfig{Workers: 4, DryRun: false, GlobalMinInterval: time.Millisecond}, stats)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	eng.Run(ctx, jobs)

	s := stats.Snapshot()
	if s.ActiveLimit != 1 {
		t.Fatalf("expected collapse to 1 worker, got ActiveLimit=%d", s.ActiveLimit)
	}
	if int(s.Deleted) != total || s.Skipped != 0 || s.Failed != 0 {
		t.Fatalf("all %d messages across every channel must be deleted after collapse; got deleted=%d skipped=%d failed=%d",
			total, s.Deleted, s.Skipped, s.Failed)
	}
}

// A per-channel ("user") 429 must not collapse; parallelism still helps there.
func TestEngineDoesNotCollapseOnUserScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Scope", "user") // per-channel bucket
		w.WriteHeader(429)
		w.Write([]byte(`{"global":false,"retry_after":0.005}`))
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	const total = 6
	ids := make([]string, total)
	for i := range ids {
		ids[i] = strconv.Itoa(3000000 + i)
	}
	stats := NewStats(total, 4)
	eng := NewEngine(EngineConfig{Workers: 4, DryRun: false, GlobalMinInterval: time.Millisecond}, stats)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	eng.Run(ctx, []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: ids}})

	if got := stats.Snapshot().ActiveLimit; got != 4 {
		t.Fatalf("per-channel (user) throttling must not collapse concurrency, got %d", got)
	}
}
