package main

import "testing"

// The reaction pacing knob shows in Advanced only for packages that have
// reactions, and edits commit (with validation) to cfg.reactionDelay.
func TestReactionDelayAdvancedField(t *testing.T) {
	m := demoModel()
	m.advanced = true

	hasField := func() bool {
		for _, f := range m.visibleFields() {
			if f.id == "reactionDelay" {
				return true
			}
		}
		return false
	}
	if hasField() {
		t.Fatal("reactionDelay must be hidden for a package without reactions")
	}
	m.caps.HasReactions = true
	if !hasField() {
		t.Fatal("reactionDelay should be an advanced field when the package has reactions")
	}

	m.commitEdit("reactionDelay", "0.55")
	if m.cfg.reactionDelay != 0.55 || m.perr != "" {
		t.Fatalf("commit: want 0.55 and no error, got %v / %q", m.cfg.reactionDelay, m.perr)
	}
	if got := m.fieldValue("reactionDelay"); got != "0.55" {
		t.Fatalf("fieldValue: want 0.55, got %q", got)
	}

	m.commitEdit("reactionDelay", "not-a-number")
	if m.perr == "" {
		t.Fatal("a non-numeric reaction delay should set perr")
	}
	if m.cfg.reactionDelay != 0.55 {
		t.Fatalf("a rejected edit must not change the value, got %v", m.cfg.reactionDelay)
	}
}
