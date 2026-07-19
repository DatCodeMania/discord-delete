package main

import (
	"testing"
	"time"
)

func TestParseSnowflake(t *testing.T) {
	if n, err := parseSnowflake(" 12345 "); err != nil || n != 12345 {
		t.Fatalf("valid id: got %d, %v", n, err)
	}
	if n, err := parseSnowflake(""); err != nil || n != 0 {
		t.Fatalf("empty means no bound: got %d, %v", n, err)
	}
	if _, err := parseSnowflake("12O45"); err == nil {
		t.Fatal("a malformed id must error, never silently drop the bound")
	}
}

func TestToSet(t *testing.T) {
	if toSet("") != nil {
		t.Fatal("empty csv should be nil (no filter)")
	}
	s := toSet("a, b,,c")
	if len(s) != 3 || !s["a"] || !s["b"] || !s["c"] {
		t.Fatalf("csv parse wrong: %v", s)
	}
}

// estimate packs channels onto workers greedily; runtime is the fullest bucket.
func TestEstimate(t *testing.T) {
	jobs := []ChannelJob{
		{MsgIDs: make([]string, 10)},
		{MsgIDs: make([]string, 10)},
	}
	if got := estimate(jobs, 2, time.Second); got != 10*time.Second {
		t.Fatalf("2 workers: want 10s, got %v", got)
	}
	if got := estimate(jobs, 1, time.Second); got != 20*time.Second {
		t.Fatalf("1 worker: want 20s, got %v", got)
	}
}

func TestNounFor(t *testing.T) {
	if nounFor("messages", 1) != "message" || nounFor("reactions", 2) != "reaction(s)" {
		t.Fatalf("nounFor wrong: %q / %q", nounFor("messages", 1), nounFor("reactions", 2))
	}
}
