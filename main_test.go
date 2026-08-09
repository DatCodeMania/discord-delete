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

// --no-tui promises a non-interactive run, so it has to rule out both interactive
// screens: a missing --package prints the help and exits rather than opening the
// picker, and the run itself falls back to the plain path, terminal or not.
func TestCanRunTUI(t *testing.T) {
	if !canRunTUI(false, true) {
		t.Fatal("a plain terminal run should reach the picker and the TUI")
	}
	if canRunTUI(true, true) {
		t.Fatal("--no-tui must stay non-interactive, even on a terminal")
	}
	if canRunTUI(false, false) {
		t.Fatal("no terminal means no interactive screen")
	}
	if canRunTUI(true, false) {
		t.Fatal("--no-tui without a terminal must stay non-interactive")
	}
}

func TestNounFor(t *testing.T) {
	if nounFor("messages", 1) != "message" || nounFor("reactions", 2) != "reaction(s)" {
		t.Fatalf("nounFor wrong: %q / %q", nounFor("messages", 1), nounFor("reactions", 2))
	}
}

// Commands posted while a headless phase wound down are applied at the boundary
// instead of being lost, and a stop wins over anything queued behind it.
func TestDrainControl(t *testing.T) {
	if stop, paused := drainControl(nil); stop || paused {
		t.Fatal("control off should change nothing")
	}
	ctrl := make(chan controlCmd, 8)
	if stop, paused := drainControl(ctrl); stop || paused {
		t.Fatal("an idle boundary should change nothing")
	}
	ctrl <- cmdPause
	if stop, paused := drainControl(ctrl); stop || !paused {
		t.Fatalf("pause at the boundary: stop=%v paused=%v", stop, paused)
	}
	ctrl <- cmdPause
	ctrl <- cmdResume
	if stop, paused := drainControl(ctrl); stop || paused {
		t.Fatalf("resume should cancel a queued pause: stop=%v paused=%v", stop, paused)
	}
	ctrl <- cmdPause
	ctrl <- cmdStop
	if stop, _ := drainControl(ctrl); !stop {
		t.Fatal("a queued stop must end the run")
	}
}
