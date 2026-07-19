package main

import (
	"testing"
	"time"
)

func TestSnowflakeRoundTrip(t *testing.T) {
	when := time.Date(2023, 6, 15, 12, 0, 0, 0, time.UTC)
	id := timeToSnowflake(when)
	got := snowflakeToTime(id)
	if d := got.Sub(when); d < -5*time.Millisecond || d > 5*time.Millisecond {
		t.Fatalf("round trip off by %v", d)
	}
}

func TestParseWindow(t *testing.T) {
	now := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"7d":    now.AddDate(0, 0, -7),
		"2w":    now.AddDate(0, 0, -14),
		"week":  now.AddDate(0, 0, -7),
		"3mo":   now.AddDate(0, -3, 0),
		"month": now.AddDate(0, -1, 0),
		"1y":    now.AddDate(-1, 0, 0),
		"year":  now.AddDate(-1, 0, 0),
		"24h":   now.Add(-24 * time.Hour),
	}
	for in, want := range cases {
		got, err := parseWindow(in, now)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if !got.Equal(want) {
			t.Fatalf("%q: got %v want %v", in, got, want)
		}
	}
	if _, err := parseWindow("banana", now); err == nil {
		t.Fatalf("expected error for bad window")
	}
}

func TestResolveBoundsTightest(t *testing.T) {
	now := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	// With --last 1y and --after-date 2026-01-01, the later date (Jan 2026) wins.
	tb, err := resolveBounds("", "", "2026-01-01", "", "1y", now)
	if err != nil {
		t.Fatal(err)
	}
	want := timeToSnowflake(mustDate(t, "2026-01-01"))
	if tb.AfterID != want {
		t.Fatalf("after bound: got %d want %d", tb.AfterID, want)
	}
	if _, err := resolveBounds("", "", "2026-06-01", "2026-01-01", "", now); err == nil {
		t.Fatalf("expected empty-range error")
	}
}

func TestSortMsgIDs(t *testing.T) {
	// Three IDs, out of order (older snowflakes are smaller).
	ids := []string{"3000000000000000000", "1000000000000000000", "2000000000000000000"}
	oldest := append([]string(nil), ids...)
	sortMsgIDs(oldest, "oldest")
	if oldest[0] != "1000000000000000000" || oldest[2] != "3000000000000000000" {
		t.Fatalf("oldest-first wrong: %v", oldest)
	}
	newest := append([]string(nil), ids...)
	sortMsgIDs(newest, "newest")
	if newest[0] != "3000000000000000000" || newest[2] != "1000000000000000000" {
		t.Fatalf("newest-first wrong: %v", newest)
	}
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := parseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// "today" means since local midnight; "day" stays a rolling 24-hour window.
func TestParseWindowTodaySinceMidnight(t *testing.T) {
	now := time.Date(2026, 7, 13, 9, 30, 0, 0, time.Local)
	got, err := parseWindow("today", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 13, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("today: want %v, got %v", want, got)
	}
	got, err = parseWindow("day", now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now.AddDate(0, 0, -1)) {
		t.Fatalf("day: want now-24h, got %v", got)
	}
}

// A malformed snowflake bound must error out, never silently widen the range.
func TestResolveBoundsRejectsMalformedSnowflake(t *testing.T) {
	if _, err := resolveBounds("12345O67", "", "", "", "", time.Now()); err == nil {
		t.Fatal("malformed after-snowflake must error")
	}
	if _, err := resolveBounds("", "12x", "", "", "", time.Now()); err == nil {
		t.Fatal("malformed before-snowflake must error")
	}
}
