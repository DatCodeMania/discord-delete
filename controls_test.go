package main

import (
	"context"
	"testing"
	"time"
)

// Pausing parks gate() until resumed.
func TestLimiterPauseBlocksAndResumes(t *testing.T) {
	l := &limiter{}
	l.paused.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan bool, 1)
	go func() { got <- l.gate(ctx) }()

	select {
	case <-got:
		t.Fatal("gate must block while paused")
	case <-time.After(300 * time.Millisecond):
	}

	l.paused.Store(false)
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("gate should return true after resume")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate did not resume after unpause")
	}
}

func TestLimiterPauseReleasesOnCancel(t *testing.T) {
	l := &limiter{}
	l.paused.Store(true)
	ctx, cancel := context.WithCancel(context.Background())

	got := make(chan bool, 1)
	go func() { got <- l.gate(ctx) }()
	cancel()

	select {
	case ok := <-got:
		if ok {
			t.Fatal("gate should return false when ctx is cancelled while paused")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled gate did not unblock")
	}
}

// A global-429 pause that lands while a worker is already sleeping on its
// account-wide spacing slot must still be honored: the request may not fire
// inside the pause window.
func TestLimiterGateHonorsPauseArrivingMidSlot(t *testing.T) {
	l := &limiter{minInterval: 300 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Take the first slot so the next gate call sleeps on spacing.
	if !l.gate(ctx) {
		t.Fatal("first gate should pass")
	}

	got := make(chan bool, 1)
	go func() { got <- l.gate(ctx) }()

	// Land the pause while the worker sleeps on its ~300ms spacing slot.
	time.Sleep(100 * time.Millisecond)
	l.pauseGlobal(1200 * time.Millisecond)
	pauseEnd := time.Now().Add(1200 * time.Millisecond)

	// Well past the spacing slot but well inside the pause window: the old
	// code would already have fired here.
	select {
	case <-got:
		t.Fatal("gate fired inside the pause window")
	case <-time.After(700 * time.Millisecond):
	}

	select {
	case ok := <-got:
		if !ok {
			t.Fatal("gate should return true once the pause expires")
		}
		if time.Now().Before(pauseEnd.Add(-50 * time.Millisecond)) {
			t.Fatal("gate returned before the pause window ended")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gate never returned after the pause expired")
	}
}

func TestTogglePaused(t *testing.T) {
	e := NewEngine(EngineConfig{Workers: 1, DeleteDelay: time.Second}, NewStats(0, 1))
	if e.isPaused() {
		t.Fatal("should start unpaused")
	}
	if !e.togglePaused() || !e.isPaused() {
		t.Fatal("toggle should pause")
	}
	if e.togglePaused() || e.isPaused() {
		t.Fatal("toggle should resume")
	}
}

func TestNudgeDelayClamps(t *testing.T) {
	e := NewEngine(EngineConfig{Workers: 1, DeleteDelay: time.Second}, NewStats(0, 1))
	if e.baseDelay() != time.Second {
		t.Fatalf("initial floor = %v, want 1s", e.baseDelay())
	}
	if got := e.nudgeDelay(500 * time.Millisecond); got != 1500*time.Millisecond {
		t.Fatalf("nudge up = %v, want 1.5s", got)
	}
	if got := e.nudgeDelay(-5 * time.Second); got != 0 {
		t.Fatalf("nudge below zero should clamp to 0, got %v", got)
	}
	if got := e.nudgeDelay(100 * time.Second); got != 30*time.Second {
		t.Fatalf("nudge above ceiling should clamp to 30s, got %v", got)
	}
}
