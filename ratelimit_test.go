package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// Regression for the false-abort bug: a sustained wall of 429s must never trip
// the invalid-request abort. 429 means slow down, not a dead token, so the run
// should back off and defer messages instead of hard-stopping.
func TestRateLimitStormDoesNotAbort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Scope", "user") // per-account bucket limit
		w.WriteHeader(429)
		w.Write([]byte(`{"global":false,"retry_after":0.005}`))
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	// 6 messages, up to 12 retries each, is about 70 individual 429s: well more
	// than invalidAbortThreshold (20), so if 429s fed the abort it would trip here.
	const total = 6
	ids := make([]string, total)
	for i := range ids {
		ids[i] = strconv.Itoa(1000000 + i)
	}
	stats := NewStats(total, 2)
	eng := NewEngine(EngineConfig{Workers: 2, DryRun: false, GlobalMinInterval: time.Millisecond}, stats)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	eng.Run(ctx, []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: ids}})

	s := stats.Snapshot()
	if s.Aborted {
		t.Fatal("a wall of 429s must NOT abort the run")
	}
	if s.Failed != 0 {
		t.Fatalf("429s must not fail messages, got failed=%d", s.Failed)
	}
	if s.Skipped != total {
		t.Fatalf("all persistently-throttled messages should be deferred (skipped), got skipped=%d", s.Skipped)
	}
}

// 401s must still abort: that's the case the invalid-budget guard exists for.
func TestUnauthorizedStillAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	ids := make([]string, 200)
	for i := range ids {
		ids[i] = strconv.Itoa(1000000 + i)
	}
	stats := NewStats(len(ids), 4)
	eng := NewEngine(EngineConfig{Workers: 4, DryRun: false, GlobalMinInterval: time.Millisecond}, stats)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.Run(ctx, []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: ids}})

	if !stats.Snapshot().Aborted {
		t.Fatal("a wall of 401s must still abort (dead/expired token)")
	}
}

func TestPauseGlobalTakesMax(t *testing.T) {
	l := &limiter{}
	l.pauseGlobal(200 * time.Millisecond)
	a := l.pauseUntilMs.Load()
	l.pauseGlobal(5 * time.Millisecond) // shorter, must not shrink the active pause
	if b := l.pauseUntilMs.Load(); b != a {
		t.Fatalf("a shorter pause must not shrink the active one: was %d, became %d", a, b)
	}
	l.pauseGlobal(2 * time.Second) // longer, must extend
	if c := l.pauseUntilMs.Load(); c <= a {
		t.Fatalf("a longer pause must extend: was %d, stayed %d", a, c)
	}
}
