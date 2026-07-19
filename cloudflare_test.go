package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func newTestCF(pauseAt, resumeAt int) *cfBudget {
	return &cfBudget{stats: NewStats(0, 1), window: time.Minute, pauseAt: pauseAt, resumeAt: resumeAt}
}

func TestCFBudgetUnderThreshold(t *testing.T) {
	b := newTestCF(5, 3)
	now := time.Unix(1_000_000, 0)
	for i := 0; i < 4; i++ {
		b.record(now)
	}
	if paused, _ := b.decide(now); paused {
		t.Fatal("4 invalids under a threshold of 5 must not pause")
	}
}

func TestCFBudgetPausesAndComputesWake(t *testing.T) {
	b := newTestCF(5, 3)
	now := time.Unix(1_000_000, 0)
	for i := 0; i < 5; i++ {
		b.record(now)
	}
	paused, wake := b.decide(now)
	if !paused {
		t.Fatal("5 invalids at a threshold of 5 must pause")
	}
	// drop = n - resumeAt + 1 = 5 - 3 + 1 = 3; wake = times[2] + window.
	want := now.Add(b.window)
	if !wake.Equal(want) {
		t.Fatalf("wake = %v, want %v", wake, want)
	}
}

func TestCFBudgetPrunesOldEntries(t *testing.T) {
	b := newTestCF(5, 3)
	old := time.Unix(1_000_000, 0)
	for i := 0; i < 5; i++ {
		b.record(old)
	}
	later := old.Add(b.window + time.Second)
	if paused, _ := b.decide(later); paused {
		t.Fatal("all entries aged out - must not pause")
	}
	if b.count() != 0 {
		t.Fatalf("expected the window to be empty, got %d", b.count())
	}
}

// End-to-end: the engine records 401/403/429 (but not 404) toward the budget.
func TestEngineRecordsInvalidBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403) // undeletable, counts toward the invalid budget
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	const total = 6
	ids := make([]string, total)
	for i := range ids {
		ids[i] = strconv.Itoa(5000000 + i)
	}
	stats := NewStats(total, 2)
	eng := NewEngine(EngineConfig{Workers: 2, DryRun: false, GlobalMinInterval: time.Millisecond}, stats)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	eng.Run(ctx, []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: ids}})

	if got := stats.Snapshot().InvalidWindow; got != total {
		t.Fatalf("expected %d invalid responses counted, got %d", total, got)
	}
}

// A 404 ("already gone") is a success and must NOT count toward the budget.
func TestEngine404NotCountedAsInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	ids := []string{"1", "2", "3"}
	stats := NewStats(len(ids), 1)
	eng := NewEngine(EngineConfig{Workers: 1, DryRun: false, GlobalMinInterval: time.Millisecond}, stats)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.Run(ctx, []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: ids}})

	if got := stats.Snapshot().InvalidWindow; got != 0 {
		t.Fatalf("404s must not count toward the Cloudflare budget, got %d", got)
	}
}
