package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProgressLogRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "deleted.log") // dir created by openProgressLog
	pl, err := openProgressLog(path)
	if err != nil {
		t.Fatal(err)
	}
	pl.record("111")
	pl.record("222")
	pl.record("333")
	pl.close()

	set := loadProgressSet(path)
	for _, id := range []string{"111", "222", "333"} {
		if !set[id] {
			t.Fatalf("expected %s in loaded set %v", id, set)
		}
	}
	if len(set) != 3 {
		t.Fatalf("want 3 entries, got %d", len(set))
	}
}

func TestLoadProgressSetMissing(t *testing.T) {
	if s := loadProgressSet(filepath.Join(t.TempDir(), "nope.log")); len(s) != 0 {
		t.Fatalf("missing log should yield empty set, got %d", len(s))
	}
}

func TestFilterDoneExcludes(t *testing.T) {
	raws := []RawChannel{{ChannelID: "1", Label: "#c", Messages: []Message{
		newMessage("1000000000000000001", "a"),
		newMessage("1000000000000000002", "b"),
		newMessage("1000000000000000003", "c"),
	}}}
	done := map[string]bool{"1000000000000000002": true}
	_, total := ApplyFilter(raws, Filter{Done: done})
	if total != 2 {
		t.Fatalf("Done should exclude 1 message: want 2, got %d", total)
	}
}

func TestProgressPathUsesOwnerAndStateDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DISCORD_DELETE_STATE_DIR", dir)
	p := progressPath(PackageOwner{ID: "42"}, "/some/pkg.zip")
	if !strings.HasPrefix(p, dir) {
		t.Fatalf("path %q should be under state dir %q", p, dir)
	}
	if !strings.Contains(p, "user-42") {
		t.Fatalf("path %q should be keyed by owner id", p)
	}
}

func TestEngineRecordsOnlyConfirmedGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		switch id {
		case "1":
			w.WriteHeader(204)
		case "2":
			w.WriteHeader(404)
		case "3":
			w.WriteHeader(403)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	var mu sync.Mutex
	var recorded []string
	eng := NewEngine(EngineConfig{
		Workers: 1, DeleteDelay: time.Millisecond, GlobalMinInterval: time.Millisecond,
		OnDeleted: func(id string) { mu.Lock(); recorded = append(recorded, id); mu.Unlock() },
	}, NewStats(3, 1))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.Run(ctx, []ChannelJob{{ChannelID: "9", Label: "x", MsgIDs: []string{"1", "2", "3"}}})

	sort.Strings(recorded)
	got := strings.Join(recorded, ",")
	if got != "1,2" { // 204 and 404 recorded; 403 (still exists) is not
		t.Fatalf("recorded = %q, want \"1,2\" (403 must not be recorded)", got)
	}
}

// TestResumeSkipsAlreadyDeleted runs the engine twice: the first run deletes and
// logs messages (one 403 holdout stays undeleted), and the second reloads the log
// so only the holdout remains after filtering.
func TestResumeSkipsAlreadyDeleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if id == "1000000000000000003" {
			w.WriteHeader(403) // still exists; must NOT be logged
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL
	t.Cleanup(func() { apiBaseOverride = "" })

	raws := []RawChannel{{ChannelID: "9", Label: "#c", Messages: []Message{
		newMessage("1000000000000000001", "a"),
		newMessage("1000000000000000002", "b"),
		newMessage("1000000000000000003", "c"),
	}}}
	path := filepath.Join(t.TempDir(), "deleted.log")

	pl, err := openProgressLog(path)
	if err != nil {
		t.Fatal(err)
	}
	jobs1, _ := ApplyFilter(raws, Filter{})
	eng := NewEngine(EngineConfig{
		Workers: 1, DeleteDelay: time.Millisecond, GlobalMinInterval: time.Millisecond,
		OnDeleted: pl.record,
	}, NewStats(3, 1))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.Run(ctx, jobs1)
	pl.close()

	done := loadProgressSet(path)
	jobs2, total2 := ApplyFilter(raws, Filter{Done: done})
	if total2 != 1 {
		t.Fatalf("resume: want 1 message left (the 403), got %d", total2)
	}
	if len(jobs2) != 1 || len(jobs2[0].MsgIDs) != 1 || jobs2[0].MsgIDs[0] != "1000000000000000003" {
		t.Fatalf("resume: the remaining message should be the undeleted 403 one, got %+v", jobs2)
	}
}

func TestCountInSet(t *testing.T) {
	raws := []RawChannel{{ChannelID: "1", Messages: []Message{
		newMessage("100", "a"), newMessage("200", "b"), newMessage("300", "c"),
	}}}
	if n := countInSet(raws, map[string]bool{"100": true, "300": true, "999": true}); n != 2 {
		t.Fatalf("countInSet: want 2 (only 100,300 are in the package), got %d", n)
	}
}

// After an append fails, the error is surfaced through writeErr and later
// records are dropped rather than silently pretended written; records flushed
// before the failure survive on disk.
func TestProgressLogSurfacesWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deleted.log")
	pl, err := openProgressLog(path)
	if err != nil {
		t.Fatal(err)
	}
	pl.record("111")
	pl.f.Close() // kill the file under the writer, as a full or yanked disk would
	pl.record("222")
	if pl.writeErr() == nil {
		t.Fatal("a failed append must surface through writeErr")
	}
	pl.close()
	if pl.writeErr() == nil {
		t.Fatal("writeErr must persist after close")
	}
	if set := loadProgressSet(path); !set["111"] || set["222"] {
		t.Fatalf("want only the pre-failure record on disk, got %v", set)
	}
}

func TestProbeProgressLog(t *testing.T) {
	dir := t.TempDir()
	if err := probeProgressLog(filepath.Join(dir, "sub", "x.deleted.log")); err != nil {
		t.Fatalf("probe should pass for a creatable log: %v", err)
	}
	blocker := filepath.Join(dir, "f")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if probeProgressLog(filepath.Join(blocker, "x.deleted.log")) == nil {
		t.Fatal("probe should fail when the log directory cannot be created")
	}
}
