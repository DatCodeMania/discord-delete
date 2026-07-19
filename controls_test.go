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
