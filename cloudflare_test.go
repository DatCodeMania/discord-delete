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
	return &cfBudget{window: time.Minute, pauseAt: pauseAt, resumeAt: resumeAt}
}

func TestCFBudgetUnderThreshold(t *testing.T) {
	b := newTestCF(5, 3)
	st := NewStats(0, 1)
	now := time.Unix(1_000_000, 0)
	for i := 0; i < 4; i++ {
		b.record(now, st)
	}
	if paused, _ := b.decide(now); paused {
		t.Fatal("4 invalids under a threshold of 5 must not pause")
	}
}

func TestCFBudgetPausesAndComputesWake(t *testing.T) {
	b := newTestCF(5, 3)
	st := NewStats(0, 1)
	now := time.Unix(1_000_000, 0)
	for i := 0; i < 5; i++ {
		b.record(now, st)
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
	st := NewStats(0, 1)
	old := time.Unix(1_000_000, 0)
	for i := 0; i < 5; i++ {
		b.record(old, st)
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

// The window is per IP, so the next phase continues the count instead of
// starting from zero and doubling what the guard lets through.
func TestCFBudgetCarriesAcrossPhases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	cf := newCFBudget()
	phase := func(ids []string) *Stats {
		stats := NewStats(len(ids), 1)
		eng := NewEngine(EngineConfig{Workers: 1, DryRun: false, GlobalMinInterval: time.Millisecond, CF: cf}, stats)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		eng.Run(ctx, []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: ids}})
		return stats
	}
	phase([]string{"1", "2", "3"})
	second := phase([]string{"4", "5"})
	if got := second.Snapshot().InvalidWindow; got != 5 {
		t.Fatalf("second phase should continue the window: got %d, want 5", got)
	}
	if got := cf.count(); got != 5 {
		t.Fatalf("shared budget = %d, want 5", got)
	}

	// The inherited count shows before the new phase spends anything.
	fresh := NewStats(0, 1)
	NewEngine(EngineConfig{Workers: 1, CF: cf}, fresh)
	if got := fresh.Snapshot().InvalidWindow; got != 5 {
		t.Fatalf("a new phase should start showing %d invalids, got %d", 5, got)
	}
}

// A nil CF keeps each engine's window to itself.
func TestCFBudgetPrivateByDefault(t *testing.T) {
	a := NewEngine(EngineConfig{Workers: 1}, NewStats(0, 1))
	b := NewEngine(EngineConfig{Workers: 1}, NewStats(0, 1))
	if a.cf == b.cf {
		t.Fatal("engines without a CF must not share one budget")
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
