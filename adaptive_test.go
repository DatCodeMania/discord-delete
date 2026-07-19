package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdaptSpacingBackoffAndRecovery(t *testing.T) {
	floor := time.Second

	// Throttle backs off multiplicatively from the floor.
	if got := adaptSpacing(floor, floor, true); got != 1600*time.Millisecond {
		t.Fatalf("first backoff: want 1.6s, got %v", got)
	}
	// …and keeps climbing, but is capped at floor*ceilMult.
	cur := floor
	for i := 0; i < 20; i++ {
		cur = adaptSpacing(cur, floor, true)
	}
	if cur != 8*time.Second { // ceilMult 8 * 1s
		t.Fatalf("backoff should cap at 8s, got %v", cur)
	}

	// Success recovers additively, never below the floor.
	cur = adaptSpacing(cur, floor, false)
	if cur != 8*time.Second-200*time.Millisecond {
		t.Fatalf("recovery step: want 7.8s, got %v", cur)
	}
	for i := 0; i < 100; i++ {
		cur = adaptSpacing(cur, floor, false)
	}
	if cur != floor {
		t.Fatalf("recovery should settle at the floor, got %v", cur)
	}
}

func TestAdaptSpacingCeilingAbsoluteCap(t *testing.T) {
	floor := 10 * time.Second // floor*8 = 80s, must clamp to adaptiveCeilMax (30s)
	cur := floor
	for i := 0; i < 20; i++ {
		cur = adaptSpacing(cur, floor, true)
	}
	if cur != adaptiveCeilMax {
		t.Fatalf("want ceiling clamped to %v, got %v", adaptiveCeilMax, cur)
	}
}

func TestAdaptSpacingDisabledAtZeroFloor(t *testing.T) {
	if got := adaptSpacing(0, 0, true); got != 0 {
		t.Fatalf("floor 0 must disable adaptation, got %v", got)
	}
}

func TestDeleteOneReportsThrottle(t *testing.T) {
	// 429 once, then 204: deleteOne retries internally but must still report throttled.
	var n int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&n, 1) == 1 {
			w.Header().Set("X-RateLimit-Scope", "user")
			w.WriteHeader(429)
			w.Write([]byte(`{"retry_after":0.01}`))
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	eng := NewEngine(EngineConfig{Workers: 1, DeleteDelay: time.Millisecond, GlobalMinInterval: time.Millisecond}, NewStats(1, 1))
	if !eng.deleteOne(context.Background(), "1", deleteItem{MessageID: "9", Key: "9"}, "x") {
		t.Fatal("expected deleteOne to report throttled=true after a 429")
	}

	// A clean 204 reports no throttling.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv2.Close()
	apiBaseOverride = srv2.URL
	if eng.deleteOne(context.Background(), "1", deleteItem{MessageID: "9", Key: "9"}, "x") {
		t.Fatal("expected deleteOne to report throttled=false on a clean delete")
	}
}
