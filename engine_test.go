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

func TestDeleteHappyPathAndRateLimits(t *testing.T) {
	var calls int64
	var firstGlobal int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		switch {
		case n == 2 && atomic.CompareAndSwapInt64(&firstGlobal, 0, 1):
			// One-time global 429 on the second call.
			w.Header().Set("X-RateLimit-Global", "true")
			w.Header().Set("X-RateLimit-Scope", "global")
			w.WriteHeader(429)
			w.Write([]byte(`{"global":true,"retry_after":0.05}`))
		case n == 4:
			// One-time per-bucket 429 on the fourth call.
			w.Header().Set("X-RateLimit-Scope", "user")
			w.WriteHeader(429)
			w.Write([]byte(`{"global":false,"retry_after":0.05}`))
		default:
			w.Header().Set("X-RateLimit-Remaining", "4")
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()

	apiBaseOverride = srv.URL

	stats := NewStats(5, 2)
	eng := NewEngine(EngineConfig{
		Workers: 2, DeleteDelay: 5 * time.Millisecond, Jitter: 0,
		DryRun: false, GlobalMinInterval: time.Millisecond,
	}, stats)

	jobs := []ChannelJob{
		{ChannelID: "111", Label: "a", MsgIDs: []string{"1", "2", "3"}},
		{ChannelID: "222", Label: "b", MsgIDs: []string{"4", "5"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.Run(ctx, jobs)

	snap := stats.Snapshot()
	if snap.Deleted != 5 {
		t.Fatalf("want 5 deleted, got %d (skipped %d failed %d)", snap.Deleted, snap.Skipped, snap.Failed)
	}
	if snap.Failed != 0 {
		t.Fatalf("want 0 failed, got %d", snap.Failed)
	}
	if !snap.Completed {
		t.Fatalf("a fully drained run should be Completed")
	}
}

func TestForbiddenIsSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"Missing Permissions","code":50013}`))
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	stats := NewStats(1, 1)
	eng := NewEngine(EngineConfig{Workers: 1, DryRun: false, GlobalMinInterval: time.Millisecond}, stats)
	eng.Run(context.Background(), []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: []string{"9"}}})
	s := stats.Snapshot()
	if s.Skipped != 1 {
		t.Fatalf("want 1 skipped, got skipped=%d deleted=%d failed=%d", s.Skipped, s.Deleted, s.Failed)
	}
	if s.Forbidden["1"].Count != 1 {
		t.Fatalf("403 should be recorded per-channel, got %v", s.Forbidden)
	}
	if s.Forbidden["1"].Reason != "Missing Permissions" {
		t.Fatalf("403 should capture Discord's reason, got %q", s.Forbidden["1"].Reason)
	}
}

// TestForbiddenResumeSplit covers the two kinds of 403: a system message (50021)
// is logged and never retried, lost access stays retryable. Both skip.
func TestForbiddenResumeSplit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantDone []string
	}{
		{"system message is permanent", `{"message":"Cannot execute action on a system message","code":50021}`, []string{"9"}},
		{"missing access stays retryable", `{"message":"Missing Access","code":50001}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(403)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			apiBaseOverride = srv.URL

			var done []string
			stats := NewStats(1, 1)
			eng := NewEngine(EngineConfig{
				Workers: 1, DryRun: false, GlobalMinInterval: time.Millisecond,
				OnDeleted: func(key string) { done = append(done, key) },
			}, stats)
			eng.Run(context.Background(), []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: []string{"9"}}})

			if s := stats.Snapshot(); s.Skipped != 1 || s.Deleted != 0 {
				t.Fatalf("a 403 is always a skip, never a delete: skipped=%d deleted=%d", s.Skipped, s.Deleted)
			}
			if len(done) != len(tc.wantDone) {
				t.Fatalf("resume log got %v, want %v", done, tc.wantDone)
			}
			for i := range tc.wantDone {
				if done[i] != tc.wantDone[i] {
					t.Fatalf("resume log got %v, want %v", done, tc.wantDone)
				}
			}
		})
	}
}

func TestParseAPIErrorCode(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"message":"Cannot execute action on a system message","code":50021}`, errSystemMessage},
		{`{"message":"Missing Access","code":50001}`, 50001},
		{`{"message":"no code here"}`, 0},
		{``, 0},
		{`not json`, 0},
	} {
		if got := parseAPIErrorCode([]byte(tc.body)); got != tc.want {
			t.Errorf("parseAPIErrorCode(%q) = %d, want %d", tc.body, got, tc.want)
		}
	}
}

// TestStoppedRunIsNotCompleted confirms the completed/stopped fix: a run whose
// context is cancelled mid-stream returns with Finished true but Completed false.
func TestStoppedRunIsNotCompleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "5")
		w.WriteHeader(204)
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL

	ids := make([]string, 1000)
	for i := range ids {
		ids[i] = strconv.Itoa(i)
	}
	stats := NewStats(len(ids), 1)
	eng := NewEngine(EngineConfig{Workers: 1, DeleteDelay: 20 * time.Millisecond, GlobalMinInterval: time.Millisecond}, stats)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(40 * time.Millisecond); cancel() }()
	eng.Run(ctx, []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: ids}})

	snap := stats.Snapshot()
	if !snap.Finished {
		t.Fatal("Run returned, so Finished should be true")
	}
	if snap.Completed {
		t.Fatalf("a cancelled run must not be Completed (deleted %d of %d)", snap.Deleted, snap.Total)
	}
}

// TestRecentRateSteadyThenStall drives the windowed rate with synthetic
// timestamps: a steady delete/sec reads as ~1/s, and a stall after it reads as
// zero while the run average stays positive.
func TestRecentRateSteadyThenStall(t *testing.T) {
	s := NewStats(1000, 1)
	sec := int64(time.Second)
	// A coarse Windows wall clock can read the same instant at NewStats and
	// Snapshot; backdating the start keeps the run-average denominator nonzero.
	s.startNano -= 150 * sec
	for i := int64(1); i <= 120; i++ {
		s.noteDeleted(s.startNano + i*sec)
		atomic.AddInt64(&s.deleted, 1)
	}
	now := s.startNano + 120*sec
	if r := s.recentRate(now); r < 0.9 || r > 1.1 {
		t.Fatalf("steady 1/s should read ~1.0, got %.3f", r)
	}
	// Half the window empty halves the rate.
	if r := s.recentRate(now + 30*sec); r < 0.4 || r > 0.6 {
		t.Fatalf("30s into a stall should read ~0.5, got %.3f", r)
	}
	if r := s.recentRate(now + 2*int64(rateWindow)); r != 0 {
		t.Fatalf("a stall past the window should read 0, got %.3f", r)
	}
	if snap := s.Snapshot(); snap.Rate <= 0 {
		t.Fatalf("run average must stay positive through a stall, got %.3f", snap.Rate)
	}
}

// TestSnapshotCarriesRecentRate checks the wiring: Snapshot().RecentRate is
// the windowed rate, so deletions aged past the window drop it to zero while
// the run-average Rate keeps counting them.
func TestSnapshotCarriesRecentRate(t *testing.T) {
	s := NewStats(10, 1)
	s.startNano -= int64(time.Second) // backdated so a coarse Windows clock cannot read elapsed as zero
	s.addDeleted()
	s.addDeleted()
	if snap := s.Snapshot(); snap.RecentRate <= 0 {
		t.Fatalf("RecentRate should be positive right after deletions, got %.3f", snap.RecentRate)
	}
	shift := 2 * int64(rateWindow)
	s.startNano -= shift
	s.mu.Lock()
	for i := range s.recent {
		s.recent[i] -= shift
	}
	s.mu.Unlock()
	snap := s.Snapshot()
	if snap.RecentRate != 0 {
		t.Fatalf("deletions older than the window must not count, got RecentRate %.3f", snap.RecentRate)
	}
	if snap.Rate <= 0 {
		t.Fatalf("run average must keep aged deletions, got %.3f", snap.Rate)
	}
}
