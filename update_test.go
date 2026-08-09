package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.2", "1.2.0", 0},
		{"1.2.3", "1.2.4", -1},
		{"1.2.4", "1.2.3", 1},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0", "10.0.0", -1},
		{"0.9", "1.0.0", -1},
		{"1.0.0-rc1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc1", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-rc1", "1.0.0-rc1", 0},
		{"v2.1.0-beta", "2.1.0-beta", 0},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// updateServerFunc points the check at a stub latest-release handler.
func updateServerFunc(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	orig := updateLatestURL
	updateLatestURL = srv.URL
	t.Cleanup(func() { updateLatestURL = orig; srv.Close() })
}

// updateServer stubs the latest-release endpoint and counts requests.
func updateServer(t *testing.T, tag string) *atomic.Int64 {
	t.Helper()
	var hits atomic.Int64
	updateServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"tag_name":"` + tag + `"}`))
	})
	return &hits
}

func TestUpdateNoticeNewer(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	updateServer(t, "v2.0.0")
	got := updateNotice("1.0.0", time.Now())
	if !strings.Contains(got, "2.0.0") || !strings.Contains(got, "1.0.0") {
		t.Fatalf("notice should name both versions, got %q", got)
	}
	if !strings.Contains(got, updateReleasesPage) {
		t.Fatalf("notice should link the releases page, got %q", got)
	}
}

func TestUpdateNoticeUpToDate(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	updateServer(t, "v1.0.0")
	if got := updateNotice("1.0.0", time.Now()); got != "" {
		t.Fatalf("up to date should yield no notice, got %q", got)
	}
}

func TestUpdateNoticeDevBuildSkipsCheck(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	hits := updateServer(t, "v9.9.9")
	if got := updateNotice("dev", time.Now()); got != "" {
		t.Fatalf("dev build should never notice, got %q", got)
	}
	if hits.Load() != 0 {
		t.Fatal("dev build should not touch the network")
	}
}

func TestUpdateNoticeOptOut(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	t.Setenv(updateCheckEnv, "1")
	hits := updateServer(t, "v9.9.9")
	if got := updateNotice("1.0.0", time.Now()); got != "" {
		t.Fatalf("opt-out should yield no notice, got %q", got)
	}
	if hits.Load() != 0 {
		t.Fatal("opt-out should not touch the network")
	}
}

// Within 24h the cached result answers; no second request is made, but the
// notice still shows.
func TestUpdateNoticeThrottledByCache(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	hits := updateServer(t, "v2.0.0")
	now := time.Now()
	if got := updateNotice("1.0.0", now); got == "" {
		t.Fatal("first check should notice")
	}
	if got := updateNotice("1.0.0", now.Add(time.Hour)); got == "" {
		t.Fatal("cached check should still notice")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("wanted exactly 1 request, got %d", n)
	}
	// Past the window it checks again.
	if got := updateNotice("1.0.0", now.Add(25*time.Hour)); got == "" {
		t.Fatal("fresh check should notice")
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("wanted a second request after 24h, got %d", n)
	}
}

// A dead endpoint yields no notice and no error, and the failed attempt is
// cached so the next launch inside the window stays offline-quiet.
func TestUpdateNoticeOfflineIsQuiet(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // immediately dead: connection refused
	orig := updateLatestURL
	updateLatestURL = srv.URL
	t.Cleanup(func() { updateLatestURL = orig })

	now := time.Now()
	if got := updateNotice("1.0.0", now); got != "" {
		t.Fatalf("failed check should be silent, got %q", got)
	}
	checkedAt, tag := readUpdateCache(updateCachePathForTest())
	if checkedAt.IsZero() || tag != "" {
		t.Fatalf("failed attempt should cache its time (got %v) with no tag (got %q)", checkedAt, tag)
	}
}

func updateCachePathForTest() string {
	return progressDir() + "/update-check"
}

// Long release notes and a big asset list make a legitimate payload hundreds of
// KB, well inside the cap, so it has to parse rather than clip to "no release".
func TestFetchLatestTagReadsLargeRelease(t *testing.T) {
	notes := strings.Repeat("a", 300<<10)
	updateServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v2.0.0","body":"` + notes + `"}`))
	})
	tag, err := fetchLatestTag(context.Background())
	if err != nil {
		t.Fatalf("large release should parse: %v", err)
	}
	if tag != "v2.0.0" {
		t.Fatalf("tag = %q, want v2.0.0", tag)
	}
}

// The padding is whitespace, so clipping the body at the cap would leave JSON
// that parses clean: the cap has to be detected, not inferred from a parse
// failure.
func TestFetchLatestTagRejectsOversizedBody(t *testing.T) {
	pad := strings.Repeat(" ", updateMaxBody)
	updateServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v2.0.0"}` + pad))
	})
	tag, err := fetchLatestTag(context.Background())
	if err == nil {
		t.Fatalf("body past the cap should error, got tag %q", tag)
	}
	if tag != "" {
		t.Fatalf("failed fetch should yield no tag, got %q", tag)
	}
}

// A failed check must expire on the retry interval, not the daily one, or one
// bad response silences the notice for a day and every day after it.
func TestUpdateNoticeFailedCheckRetriesSooner(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	var hits atomic.Int64
	updateServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"tag_name":"v2.0.0"}`))
	})
	now := time.Now()
	if got := updateNotice("1.0.0", now); got != "" {
		t.Fatalf("failed check should be silent, got %q", got)
	}
	if got := updateNotice("1.0.0", now.Add(time.Minute)); got != "" {
		t.Fatalf("failed check should be silent, got %q", got)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("failure should not re-probe every launch, got %d requests", n)
	}
	got := updateNotice("1.0.0", now.Add(updateRetryEvery+time.Minute))
	if !strings.Contains(got, "2.0.0") {
		t.Fatalf("check after the retry window should notice, got %q", got)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("wanted exactly 2 requests, got %d", n)
	}
}
