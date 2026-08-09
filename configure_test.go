package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

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

// bigTreeModel builds a selector model with one server of 30 numbered channels
// plus a single #zebra, so a search can narrow a long list down to one match.
func bigTreeModel() *appModel {
	var raws []RawChannel
	sel := map[string]bool{}
	add := func(id, label string) {
		raws = append(raws, RawChannel{ChannelID: id, Label: label, GuildID: "g1", GuildName: "My Server",
			Messages: []Message{newMessage("1000000000000000000", "hi")}})
		sel[id] = true
	}
	for i := 0; i < 30; i++ {
		add(fmt.Sprintf("c%02d", i), fmt.Sprintf("#chan-%02d (My Server)", i))
	}
	add("zeb", "#zebra (My Server)")
	cfg := runConfig{order: "oldest", workers: 4, delay: 1.1, jitter: 0.4, maxRPS: 25, delMessages: true}
	return newAppModel(raws, cfg, sel, "pkg")
}

// Typing in the search box shrinks the row set behind the tree key handler, so a
// cursor left deep in the unfiltered list must not scroll the viewport past the
// matches: a window starting past the last row renders an empty panel.
func TestTreeSearchRendersMatchesWithDeepCursor(t *testing.T) {
	m := bigTreeModel()
	m.enterChannels(scopeMessages)
	m.ccursor = 25
	m.search.Focus()
	for _, r := range "zebra" {
		m.updateChannels(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	rows := m.channelRows()
	if len(rows) != 2 { // the guild header and the one matching channel
		t.Fatalf("search rows: want 2, got %d", len(rows))
	}
	if m.ccursor >= len(rows) {
		t.Fatalf("cursor %d is off the filtered list of %d rows", m.ccursor, len(rows))
	}
	out := stripEscapes(m.treeBody(86))
	if !strings.Contains(out, "#zebra") {
		t.Fatalf("tree body must show the match, got %q", out)
	}
	if !strings.Contains(out, "My Server") {
		t.Fatalf("tree body must show the matching guild, got %q", out)
	}
	if rows[m.ccursor].chIdx == -1 {
		t.Fatal("cursor should land on the matching channel, not the guild header")
	}

	// A stale cursor must not index the filtered rows out of range.
	m.search.Blur()
	m.ccursor = 25
	m.updateChannels(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if m.selected["zeb"] {
		t.Fatal("space on the clamped cursor should have toggled the matching channel")
	}

	// Clearing the search leaves the cursor on a row of the full list.
	m.search.Focus()
	m.updateChannels(tea.KeyMsg{Type: tea.KeyEsc})
	if m.ccursor >= len(m.channelRows()) {
		t.Fatalf("cursor %d is off the cleared list", m.ccursor)
	}
	if strings.Contains(stripEscapes(m.treeBody(86)), "#zebra") {
		t.Fatal("clearing the search should collapse back to the guild header")
	}
}
