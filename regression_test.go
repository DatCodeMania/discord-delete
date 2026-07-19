package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// H1: a malformed message id (one that can't be encoded into a request URL)
// must not panic the worker. It should be skipped so the run continues.
func TestBadMessageIDIsSkippedNotPanic(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	stats := NewStats(2, 1)
	eng := NewEngine(EngineConfig{Workers: 1, DryRun: false, GlobalMinInterval: time.Millisecond}, stats)
	// "bad id" contains a space and a control character, so NewRequest fails.
	eng.Run(context.Background(), []ChannelJob{
		{ChannelID: "1", Label: "x", MsgIDs: []string{"bad id\n", "42"}},
	})

	s := stats.Snapshot()
	if s.Skipped != 1 {
		t.Fatalf("bad id should be skipped: skipped=%d deleted=%d failed=%d", s.Skipped, s.Deleted, s.Failed)
	}
	if s.Deleted != 1 {
		t.Fatalf("the good id should still be deleted: deleted=%d", s.Deleted)
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("only the good id should reach the server, got %d calls", got)
	}
}

// H2: when the invalid-response streak trips the abort, the engine must stop
// itself immediately (cancel its own context) rather than deleting the whole
// backlog while waiting for the UI to notice.
func TestAbortStopsEngineImmediately(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.WriteHeader(401) // token invalid: every request is an invalid response
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	const total = 5000
	ids := make([]string, total)
	for i := range ids {
		ids[i] = strconv.Itoa(1000000 + i)
	}
	stats := NewStats(total, 4)
	eng := NewEngine(EngineConfig{Workers: 4, DryRun: false, GlobalMinInterval: time.Millisecond}, stats)

	done := make(chan struct{})
	go func() {
		eng.Run(context.Background(), []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: ids}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("engine did not stop after abort tripped")
	}

	s := stats.Snapshot()
	if !s.Aborted {
		t.Fatal("expected aborted=true")
	}
	// The abort trips at invalidAbortThreshold; allow generous slack for the
	// in-flight requests across 4 workers, but it must be nowhere near `total`.
	if got := atomic.LoadInt64(&calls); got > invalidAbortThreshold+4*20 {
		t.Fatalf("engine kept firing after abort: %d calls (threshold %d, total %d)", got, invalidAbortThreshold, total)
	}
}

// M2: a message that is only ever rate-limited (429) must not be marked failed
// after the transient-error budget; it is left as skipped for a later run.
func TestPersistent429IsSkippedNotFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Scope", "user")
		w.WriteHeader(429)
		w.Write([]byte(`{"global":false,"retry_after":0.01}`))
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	stats := NewStats(1, 1)
	eng := NewEngine(EngineConfig{Workers: 1, DryRun: false, GlobalMinInterval: time.Millisecond}, stats)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.Run(ctx, []ChannelJob{{ChannelID: "1", Label: "x", MsgIDs: []string{"9"}}})

	s := stats.Snapshot()
	if s.Failed != 0 {
		t.Fatalf("persistent 429 must not be failed, got failed=%d", s.Failed)
	}
	if s.Skipped != 1 {
		t.Fatalf("persistent 429 should be skipped, got skipped=%d deleted=%d", s.Skipped, s.Deleted)
	}
}

// M3: a channel folder holding both messages.json and messages.csv must yield
// exactly one job (one delete per message), not two.
func TestBothFormatsInOneFolderNotDoubleCounted(t *testing.T) {
	root := t.TempDir()
	chDir := filepath.Join(root, "messages", "c123456")
	if err := os.MkdirAll(chDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(chDir, "channel.json"), `{"id":"123456","name":"general"}`)
	writeFile(t, filepath.Join(chDir, "messages.json"), `[{"ID":"111"},{"ID":"222"}]`)
	writeFile(t, filepath.Join(chDir, "messages.csv"), "ID,Contents\n111,a\n222,b\n")

	jobs, total, err := LoadPackage(root, Filter{Order: "oldest"})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("want exactly 1 job for the channel, got %d", len(jobs))
	}
	if total != 2 {
		t.Fatalf("want 2 messages (deduped), got %d", total)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Filter.Channels semantics: nil means no filter (all); a non-nil empty set
// means NONE selected. Deselecting every channel must never widen to "all".
func TestApplyFilterEmptySelectionMeansNone(t *testing.T) {
	raws := []RawChannel{{ChannelID: "1", Messages: []Message{newMessage("100", "hi")}}}
	if _, total := ApplyFilter(raws, Filter{Channels: map[string]bool{}}); total != 0 {
		t.Fatalf("empty selection: want 0 matched, got %d", total)
	}
	if _, total := ApplyFilter(raws, Filter{}); total != 1 {
		t.Fatalf("nil selection: want 1 matched, got %d", total)
	}
}
