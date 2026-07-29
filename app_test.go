package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// runContext gives a test a run context that is cancelled when the test ends.
// A phase advanced during the test spawns an engine on it, and an uncancelled
// one keeps deleting after the test returns.
func runContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

func demoModel() *appModel {
	raws := []RawChannel{
		{ChannelID: "1", Label: "#general (My Server)", GuildID: "g1", GuildName: "My Server",
			Messages: []Message{newMessage("1000000000000000000", "hello world"), newMessage("1100000000000000000", "oops typo")}},
		{ChannelID: "2", Label: "#memes (My Server)", GuildID: "g1", GuildName: "My Server",
			Messages: []Message{newMessage("1200000000000000000", "funny")}},
		{ChannelID: "3", Label: "DM with Bob", IsDM: true,
			Messages: []Message{newMessage("2000000000000000000", "oops")}},
	}
	cfg := runConfig{order: "oldest", workers: 4, delay: 1.1, jitter: 0.4, maxRPS: 25, delMessages: true}
	sel := map[string]bool{"1": true, "2": true, "3": true}
	return newAppModel(raws, cfg, sel, "pkg")
}

func TestPreviewCountsAllByDefault(t *testing.T) {
	m := demoModel()
	if m.total != 4 {
		t.Fatalf("want 4 total, got %d", m.total)
	}
	if m.selectedSet() != nil {
		t.Fatalf("all selected should yield a nil (all) filter set")
	}
}

func TestContentFilterRecomputes(t *testing.T) {
	m := demoModel()
	m.cfg.content = "oops"
	m.recompute()
	if m.total != 2 { // "oops typo" in #general + "oops" in DM
		t.Fatalf("content filter: want 2, got %d", m.total)
	}
}

func TestChannelSelectionNarrowsPreview(t *testing.T) {
	m := demoModel()
	m.selected["3"] = false
	m.recompute()
	if m.total != 3 {
		t.Fatalf("after deselecting DM: want 3, got %d", m.total)
	}
	set := m.selectedSet()
	if set == nil || set["3"] {
		t.Fatalf("selectedSet should exclude channel 3: %v", set)
	}
}

func TestToggleGuildAndSelectAll(t *testing.T) {
	m := demoModel()
	gi := -1
	for i, g := range m.guilds {
		if g.name == "My Server" {
			gi = i
		}
	}
	if gi == -1 {
		t.Fatal("My Server guild not found")
	}
	m.toggleGuild(gi) // all selected -> deselect all its channels
	if m.selected["1"] || m.selected["2"] {
		t.Fatalf("toggleGuild should have deselected both server channels")
	}
	if !m.selected["3"] {
		t.Fatalf("DM channel should be untouched")
	}
	m.setAllSelected(true)
	for _, v := range m.selected {
		if !v {
			t.Fatalf("setAllSelected(true) should select everything")
		}
	}
}

func TestGuildGroupingDMsLast(t *testing.T) {
	m := demoModel()
	if len(m.guilds) != 2 {
		t.Fatalf("want 2 guild groups (server + DM), got %d", len(m.guilds))
	}
	if !m.guilds[len(m.guilds)-1].isDM {
		t.Fatalf("DM group should be pinned last")
	}
}

func TestExecuteRequiresToken(t *testing.T) {
	m := demoModel()
	m.toggleExecute()
	if m.cfg.execute {
		t.Fatal("execute must not turn on without a token")
	}
	if m.perr == "" {
		t.Fatal("expected an error explaining the token requirement")
	}
	m.cfg.token = "tok"
	m.toggleExecute()
	if !m.cfg.execute {
		t.Fatal("execute should turn on once a token is set")
	}
}

func TestStartExecuteGoesToConfirm(t *testing.T) {
	m := demoModel()
	m.cfg.token = "tok"
	m.cfg.execute = true
	if _, _ = m.startRun(); m.screen != scConfirm {
		t.Fatalf("execute start should route to confirm, got screen %d", m.screen)
	}
}

func TestDryRunStartCompletesViaKeys(t *testing.T) {
	m := demoModel()
	// 'c' opens configure, 'esc' returns home, 's' starts the (dry) run.
	m.updateHome(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if m.screen != scConfigure {
		t.Fatalf("'c' should open configure, got %d", m.screen)
	}
	m.updateConfigure(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != scHome {
		t.Fatalf("esc should return home, got %d", m.screen)
	}
	m.updateHome(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.screen != scRunning || !m.started {
		t.Fatalf("'s' should start a dry run, screen=%d started=%v", m.screen, m.started)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m.stats.Snapshot().Finished {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := m.stats.Snapshot()
	if !snap.Finished || snap.Deleted != 4 {
		t.Fatalf("dry run should finish deleting all 4, got finished=%v deleted=%d", snap.Finished, snap.Deleted)
	}
}

// TestReactionChannelSelectorScope checks the shared tree selector drives the
// reaction channels when scoped to reactions, without touching message selection.
func TestReactionChannelSelectorScope(t *testing.T) {
	m := demoModel()
	reactions := []Reaction{
		{ChannelID: "r1", GuildID: "g9", MessageID: "1000000000000000000", EmojiName: "👍"},
		{ChannelID: "r2", GuildID: "g9", MessageID: "1000000000000000001", EmojiName: "🔥"},
	}
	m.setReactions(reactions, map[string]string{"g9": "React Server"},
		PackageCapabilities{HasMessages: true, HasReactions: true}, "")

	m.enterChannels(scopeReactions)
	if m.chanScope != scopeReactions {
		t.Fatal("entering the reaction selector should set the reaction scope")
	}
	if got := len(m.scopeRaws()); got != 2 {
		t.Fatalf("reaction scope should expose 2 channels, got %d", got)
	}

	// Clearing selection in reaction scope must not touch the message selection.
	m.setAllSelected(false)
	for id, on := range m.reactSelected {
		if on {
			t.Fatalf("reaction channel %s should be deselected", id)
		}
	}
	for _, rc := range m.raws {
		if !m.selected[rc.ChannelID] {
			t.Fatalf("message channel %s must stay selected", rc.ChannelID)
		}
	}
}

func TestChannelSearchKeyFocusesInput(t *testing.T) {
	m := demoModel()
	m.screen = scChannels
	m.updateChannels(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !m.search.Focused() {
		t.Fatal("'/' should focus the channel search input")
	}
}

// TestPeriodicNotifyPosts checks that maybePeriodicNotify posts a progress body
// with the pause/stop action buttons once the notify interval has elapsed.
func TestPeriodicNotifyPosts(t *testing.T) {
	var gotBody, gotActions string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotActions = r.Header.Get("Actions")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	m := demoModel()
	m.cfg.execute = true
	m.cfg.ntfy = srv.URL // a full URL is used verbatim by resolveNtfyURL
	m.cfg.notifyEvery = time.Minute
	m.stats = NewStats(10, 4)
	m.stats.addDeleted()
	m.lastNotify = time.Now().Add(-2 * time.Minute) // interval already elapsed

	cmd := m.maybePeriodicNotify()
	if cmd == nil {
		t.Fatal("an elapsed interval should produce a notify command")
	}
	cmd() // run it synchronously (posts to the server)

	if gotBody == "" {
		t.Fatal("progress notification was not posted")
	}
	if !strings.Contains(gotActions, "body=pause") || !strings.Contains(gotActions, "body=stop") {
		t.Errorf("progress notification missing pause/stop buttons: %q", gotActions)
	}
	// Interval hasn't re-elapsed, so an immediate second call must not fire.
	if m.maybePeriodicNotify() != nil {
		t.Error("a second call right after should be throttled")
	}
}

// TestNtfyTestPing checks that setting a topic fires a test notification and
// clearing it clears the note.
func TestNtfyTestPing(t *testing.T) {
	var hits int
	var gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotTitle = r.Header.Get("Title")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	m := demoModel()
	m.cfg.ntfy = srv.URL // full URL used verbatim
	cmd := m.startNtfyTest()
	if cmd == nil {
		t.Fatal("a non-empty topic should produce a test command")
	}
	if m.ntfyNote == "" {
		t.Error("a pending note should show while the test is in flight")
	}
	msg := cmd() // run the post synchronously
	if _, ok := msg.(ntfyTestMsg); !ok {
		t.Fatalf("test command should return ntfyTestMsg, got %T", msg)
	}
	m.Update(msg)
	if hits != 1 || gotTitle != "discord-delete" {
		t.Fatalf("test ping not delivered: hits=%d title=%q", hits, gotTitle)
	}
	if !strings.Contains(m.ntfyNote, "sent") {
		t.Errorf("success note expected, got %q", m.ntfyNote)
	}

	// Clearing the topic turns notifications off and drops the note.
	m.cfg.ntfy = ""
	if c := m.startNtfyTest(); c != nil {
		t.Error("an empty topic should not fire a test")
	}
	if m.ntfyNote != "" {
		t.Errorf("empty topic should clear the note, got %q", m.ntfyNote)
	}
}

// Deselecting every channel must match nothing, never widen back to "all".
func TestDeselectAllChannelsMatchesNothing(t *testing.T) {
	m := demoModel()
	for id := range m.selected {
		m.selected[id] = false
	}
	m.recompute()
	if m.total != 0 {
		t.Fatalf("no channels selected: want 0 matched, got %d", m.total)
	}
}

// A reactions-only setup must be startable: the start gate can't hinge on the
// message preview count alone.
func TestStartRunReactionsOnly(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	m := demoModel()
	m.setReactions([]Reaction{
		{ChannelID: "9", MessageID: "1200000000000000001", EmojiName: "👍",
			Snowflake: 1200000000000000001, HasSnow: true},
	}, nil, PackageCapabilities{HasMessages: true, HasReactions: true}, "")
	m.cfg.delMessages = false
	m.cfg.delReactions = true
	m.recompute()
	if m.reactTotal != 1 {
		t.Fatalf("precondition: want 1 reaction matched, got %d", m.reactTotal)
	}
	m.cfg.execute = false // dry run starts without the confirm screen
	_, _ = m.startRun()
	t.Cleanup(func() {
		if m.cancel != nil {
			m.cancel()
		}
	})
	if m.perr != "" {
		t.Fatalf("reactions-only run should start, got perr %q", m.perr)
	}
	if m.screen != scRunning {
		t.Fatalf("want scRunning, got %v", m.screen)
	}
}

// finishRun reloads the reaction resume set so a second run in the same
// session skips what the first just removed.
func TestFinishRunReloadsReactionResume(t *testing.T) {
	m := demoModel()
	m.reactProgPath = filepath.Join(t.TempDir(), "r.reactions.log")
	if err := os.WriteFile(m.reactProgPath, []byte("111|222|u:x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.finishRun()
	if !m.reactDone["111|222|u:x"] {
		t.Fatal("finishRun should reload the reaction resume set")
	}
}

// A stop during an earlier phase cancels the run context, so the tick must
// finalize the run instead of starting the next phase on the dead context.
func TestStopDuringPhaseDoesNotStartNextPhase(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	m := demoModel()
	m.screen = scRunning
	m.phases = []phasePlan{{kind: "messages"}, {kind: "reactions"}}
	m.phaseIdx = 0
	m.stats = NewStats(4, 1)
	m.stats.finished.Store(true) // engine wound down after a stop: neither completed nor aborted
	_, _ = m.Update(tickMsg{})
	if !m.reported {
		t.Fatal("a stopped run must finalize, not advance to the next phase")
	}
	if m.phaseIdx != 0 {
		t.Fatalf("phaseIdx advanced to %d after a stop", m.phaseIdx)
	}
	if len(m.phaseResults) != 1 {
		t.Fatalf("want the stopped phase's result recorded once, got %d", len(m.phaseResults))
	}
}

// A phase that completed normally still advances to the next one.
func TestCompletedPhaseAdvances(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	m := demoModel()
	m.screen = scRunning
	m.runCtx = runContext(t)
	m.phases = []phasePlan{{kind: "messages"}, {kind: "reactions"}}
	m.phaseIdx = 0
	m.stats = NewStats(4, 1)
	m.stats.finished.Store(true)
	m.stats.completed.Store(true)
	_, _ = m.Update(tickMsg{})
	if m.reported {
		t.Fatal("a completed non-final phase must not finalize the run")
	}
	if m.phaseIdx != 1 {
		t.Fatalf("want phaseIdx 1 after a completed phase, got %d", m.phaseIdx)
	}
}

// A stop that lands after the phase completed, in the gap before the tick that
// would start the next one, still ends the run.
func TestBoundaryStopEndsRun(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	m := demoModel()
	m.screen = scRunning
	m.runCtx = runContext(t)
	m.phases = []phasePlan{{kind: "messages"}, {kind: "reactions"}}
	m.stats = NewStats(4, 1)
	m.eng = NewEngine(EngineConfig{Workers: 1}, m.stats)
	m.stats.finished.Store(true)
	m.stats.completed.Store(true)
	_, _ = m.Update(controlMsg{cmd: cmdStop})
	_, _ = m.Update(tickMsg{})
	if !m.reported {
		t.Fatal("a stop at the phase boundary must finalize the run")
	}
	if m.phaseIdx != 0 {
		t.Fatalf("phaseIdx advanced to %d after a stop at the boundary", m.phaseIdx)
	}
}

// A pause tapped at a phase boundary holds until the next phase's engine exists.
func TestBoundaryPauseCarriesIntoNextPhase(t *testing.T) {
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	m := demoModel()
	m.cfg.execute = true
	m.screen = scRunning
	m.runCtx = runContext(t)
	m.phases = []phasePlan{{kind: "messages"}, {kind: "reactions"}}
	m.stats = NewStats(4, 1)
	m.eng = NewEngine(EngineConfig{Workers: 1}, m.stats)
	m.stats.finished.Store(true)
	m.stats.completed.Store(true)
	_, _ = m.Update(controlMsg{cmd: cmdPause})
	if !m.pausePend {
		t.Fatal("a pause at the boundary must be held for the next phase")
	}
	_, _ = m.Update(tickMsg{})
	if m.phaseIdx != 1 {
		t.Fatalf("want phaseIdx 1, got %d", m.phaseIdx)
	}
	if !m.paused || !m.eng.isPaused() {
		t.Fatal("the next phase must start paused")
	}
	if m.pausePend {
		t.Fatal("the held pause should be consumed once applied")
	}
}

// Execute is refused when the resume log can't be opened: deleting without it
// would repeat every delete on the next run.
func TestExecuteGuardRequiresWritableResumeLog(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m := demoModel()
	m.cfg.token = "tok"
	m.tokenState = tsValid
	m.progPath = filepath.Join(blocker, "x.deleted.log")
	if reason := m.executeGuard(); reason == "" {
		t.Fatal("guard should refuse when the resume log cannot open")
	}
	m.progPath = filepath.Join(dir, "x.deleted.log")
	if reason := m.executeGuard(); reason != "" {
		t.Fatalf("guard should pass with a writable log path, got %q", reason)
	}
}
