package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
)

func TestDemoModeStaticCursor(t *testing.T) {
	if got := newPickerModel().input.Cursor.Mode(); got != cursor.CursorBlink {
		t.Fatalf("without demo env, picker cursor = %v, want CursorBlink", got)
	}

	t.Setenv("DEV_DISCORD_DELETE_DEMO", "1")
	if got := newPickerModel().input.Cursor.Mode(); got != cursor.CursorStatic {
		t.Fatalf("under demo env, picker cursor = %v, want CursorStatic", got)
	}
	m := newAppModel(nil, runConfig{}, map[string]bool{}, "pkg")
	if got := m.input.Cursor.Mode(); got != cursor.CursorStatic {
		t.Fatalf("under demo env, app input cursor = %v, want CursorStatic", got)
	}
	if got := m.search.Cursor.Mode(); got != cursor.CursorStatic {
		t.Fatalf("under demo env, app search cursor = %v, want CursorStatic", got)
	}
}

func testModel() *appModel {
	raws := []RawChannel{
		{ChannelID: "1", Label: "#general (My Server)", GuildID: "g1", GuildName: "My Server",
			Messages: []Message{newMessage("1000000000000000000", "hi")}},
		{ChannelID: "2", Label: "DM with Bob", IsDM: true,
			Messages: []Message{newMessage("2000000000000000000", "yo")}},
	}
	cfg := runConfig{order: "oldest", workers: 4, delay: 1.1, jitter: 0.4, maxRPS: 25}
	sel := map[string]bool{"1": true, "2": true}
	return newAppModel(raws, cfg, sel, "package.zip")
}

func TestRunningViewRendersWithoutPanic(t *testing.T) {
	m := testModel()
	stats := NewStats(100, 4)
	stats.addDeleted()
	stats.addSkipped()
	stats.addFailed()
	stats.setWorker(0, WorkerStatus{Active: true, Channel: "#general (My Server)", Done: 3, Total: 40})
	stats.setWorker(1, WorkerStatus{Active: false})
	stats.logErr("delete 123: HTTP 500 boom")
	stats.setStatus("429 (user) on #general - backing off 1.2s")
	m.stats = stats
	m.screen = scRunning

	out, _ := m.viewRunning()
	if !strings.Contains(out, "discord-delete") || !strings.Contains(out, "deleted") {
		t.Fatalf("running view missing expected content:\n%s", out)
	}
}

// TestRateUnitFollowsPhase checks the Rate panel's unit tracks the phase kind.
func TestRateUnitFollowsPhase(t *testing.T) {
	m := testModel()
	m.stats = NewStats(10, 1)
	m.screen = scRunning

	out, _ := m.viewRunning()
	if !strings.Contains(out, "msgs/s") {
		t.Fatalf("message phase should label the rate msgs/s:\n%s", out)
	}

	m.phases = []phasePlan{{kind: "reactions"}}
	m.phaseIdx = 0
	out, _ = m.viewRunning()
	if !strings.Contains(out, "reactions/s") || strings.Contains(out, "msgs/s") {
		t.Fatalf("reactions phase should label the rate reactions/s:\n%s", out)
	}
}

// finishedModel is parked on the final frame of a completed run. The width keeps
// the path on one unwrapped line, the height keeps the frame on screen.
func finishedModel() *appModel {
	m := testModel()
	m.stats = NewStats(4, 1)
	m.stats.finished.Store(true)
	m.stats.completed.Store(true)
	m.screen = scRunning
	m.width, m.height = 100, 200
	m.reportPath = filepath.Join("state", "run-20260731-120000.report.txt")
	return m
}

// stubOpen replaces the opener for one test and reports what a click asked for.
func stubOpen(t *testing.T, err error) *string {
	t.Helper()
	var got string
	prev := openReport
	openReport = func(p string) tea.Cmd {
		return func() tea.Msg { got = p; return openDoneMsg{p, err} }
	}
	t.Cleanup(func() { openReport = prev })
	return &got
}

// deliver drives msg through the running screen and then runs whatever command
// came back, feeding its result in as the event loop would. Opening the report
// only reaches the opener once that command runs.
func deliver(m *appModel, msg tea.Msg) {
	_, cmd := m.updateRunning(msg)
	if cmd == nil {
		return
	}
	if out := cmd(); out != nil {
		m.Update(out)
	}
}

// stripEscapes drops SGR and OSC sequences so a rendered line can be indexed by
// visible column. Hand-rolled because charmbracelet/x/ansi is an indirect
// dependency only, and an OSC 8 link is more than an SGR regex can handle.
func stripEscapes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != 0x1b || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		switch s[i+1] {
		case '[': // CSI: parameters up to a final byte in 0x40..0x7e
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			i++
		case ']': // OSC: runs to a BEL or a string terminator
			i += 2
			for i < len(s) {
				if s[i] == 0x07 {
					break
				}
				if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
					i++
					break
				}
				i++
			}
			i++
		default:
			i += 2
		}
	}
	return b.String()
}

func TestRunningViewLinksReportPath(t *testing.T) {
	m := finishedModel()

	out, _ := m.viewRunning()
	if !strings.Contains(out, "\x1b]8;;"+fileURL(m.reportPath)+"\x1b\\") {
		t.Fatalf("report path is not a hyperlink:\n%q", out)
	}
	if !strings.Contains(out, m.reportPath) {
		t.Fatalf("the path itself should still be readable:\n%q", out)
	}
}

// A plain click has to work: terminals keep the OSC 8 link for ctrl/cmd+click.
func TestFinishedFrameButtonOpensOnPlainClick(t *testing.T) {
	m := finishedModel()
	frame, btn := m.viewRunning()
	lines := strings.Split(frame, "\n")

	// A hit box that disagrees with the rendered frame sends clicks to nowhere.
	if btn.y >= len(lines) || !strings.Contains(lines[btn.y], btnOpenReport) {
		t.Fatalf("row %d does not hold the button", btn.y)
	}
	cells := []rune(stripEscapes(lines[btn.y]))
	if btn.x0 < 0 || btn.x1 >= len(cells) {
		t.Fatalf("hit box columns %d..%d fall outside the %d rendered cells", btn.x0, btn.x1, len(cells))
	}
	if got := string(cells[btn.x0 : btn.x1+1]); got != btnOpenReport {
		t.Errorf("columns %d..%d render %q, want %q", btn.x0, btn.x1, got, btnOpenReport)
	}

	got := stubOpen(t, nil)
	click := func(x, y int) {
		*got = ""
		deliver(m, tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	}

	click(btn.x0, btn.y)
	if *got != m.reportPath {
		t.Errorf("click opened %q, want %q", *got, m.reportPath)
	}
	click(btn.x1, btn.y) // the last column is still inside
	if *got != m.reportPath {
		t.Errorf("click on the last column opened %q", *got)
	}

	click(btn.x1+1, btn.y)
	if *got != "" {
		t.Errorf("click past the button opened %q", *got)
	}
	click(btn.x0, btn.y+1)
	if *got != "" {
		t.Errorf("click on the next row opened %q", *got)
	}

	*got = ""
	deliver(m, tea.MouseMsg{X: btn.x0, Y: btn.y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	deliver(m, tea.MouseMsg{X: btn.x0, Y: btn.y, Action: tea.MouseActionPress, Button: tea.MouseButtonRight})
	if *got != "" {
		t.Errorf("only a left press should open; got %q", *got)
	}
}

// The keyboard fallback, for terminals that report no mouse.
func TestFinishedFrameOpensOnKey(t *testing.T) {
	m := finishedModel()
	got := stubOpen(t, nil)

	deliver(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if *got != m.reportPath {
		t.Errorf("o opened %q, want %q", *got, m.reportPath)
	}

	// Mid-run there is no report yet, so the key does nothing.
	live := testModel()
	live.stats, live.screen = NewStats(4, 1), scRunning
	*got = ""
	deliver(live, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if *got != "" {
		t.Errorf("o during a run opened %q", *got)
	}
}

// Bubble Tea paints only the last height lines, so the row has to move with them.
func TestReportButtonRowShiftsWhenFrameOverflows(t *testing.T) {
	m := finishedModel()
	frame, btn := m.viewRunning()
	full := strings.Split(frame, "\n")
	base := btn.y

	m.height = len(full) - 3
	_, btn = m.viewRunning()
	visible := full[len(full)-m.height:] // the slice Bubble Tea actually paints

	got := btn.y
	if got != base-3 {
		t.Fatalf("row = %d, want %d once 3 lines scroll off the top", got, base-3)
	}
	if got < 0 || got >= len(visible) || !strings.Contains(visible[got], btnOpenReport) {
		t.Errorf("shifted row %d does not hold the button", got)
	}
}

func TestFinishedFrameShowsOpenFailure(t *testing.T) {
	m := finishedModel()
	_, btn := m.viewRunning()
	stubOpen(t, errors.New("no xdg-open"))

	deliver(m, tea.MouseMsg{
		X: btn.x0, Y: btn.y,
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
	})
	if out, _ := m.viewRunning(); !strings.Contains(out, "no xdg-open") {
		t.Errorf("open failure not surfaced:\n%s", out)
	}
}

// cmdMsgTypes names the messages cmd produces, flattening a batch. Bubble Tea's
// mouse messages are unexported, so their type name is the only handle a test
// has on them; the dependency is pinned, so a rename cannot arrive unseen.
func cmdMsgTypes(cmd tea.Cmd) []string {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []string{fmt.Sprintf("%T", msg)}
	}
	var out []string
	for _, c := range batch {
		out = append(out, cmdMsgTypes(c)...)
	}
	return out
}

// The mouse is grabbed for the one frame that has something clickable on it, so
// selecting text keeps working everywhere else.
func TestFinishedFrameScopesMouseCapture(t *testing.T) {
	m := testModel()
	m.reportOverride = filepath.Join(t.TempDir(), "run.report.txt")

	types := cmdMsgTypes(m.finalizeRun())
	if m.reportPath == "" {
		t.Fatal("the report did not write, so the rest proves nothing")
	}
	if !slices.Contains(types, "tea.enableMouseCellMotionMsg") {
		t.Errorf("finalizing produced %v, none of which captures the mouse", types)
	}

	// No report file, no button: capturing the mouse would only cost the user
	// their text selection.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unwritable := testModel()
	unwritable.reportOverride = filepath.Join(blocker, "run.report.txt")
	if types := cmdMsgTypes(unwritable.finalizeRun()); len(types) > 0 {
		t.Errorf("a failed report write produced %v", types)
	}
	if unwritable.reportPath != "" {
		t.Errorf("report path is %q after a failed write", unwritable.reportPath)
	}

	// Back on the home screen nothing is clickable, so the terminal gets the
	// mouse back.
	_, cmd := finishedModel().updateRunning(tea.KeyMsg{Type: tea.KeyEsc})
	if types := cmdMsgTypes(cmd); !slices.Contains(types, "tea.disableMouseMsg") {
		t.Errorf("leaving the finished run produced %v, none of which releases the mouse", types)
	}
}

func TestFileURL(t *testing.T) {
	in, want := "/tmp/dd state/run 1.report.txt", "file:///tmp/dd%20state/run%201.report.txt"
	if runtime.GOOS == "windows" {
		in, want = `C:\dd state\run 1.report.txt`, "file:///C:/dd%20state/run%201.report.txt"
	}
	if got := fileURL(in); got != want {
		t.Fatalf("fileURL(%q) = %q, want %q", in, got, want)
	}
	// Relative paths still resolve to an absolute URL the terminal can open.
	if got := fileURL("report.txt"); !strings.HasPrefix(got, "file:///") || strings.HasSuffix(got, "//report.txt") {
		t.Fatalf("relative path not made absolute: %q", got)
	}
	// A UNC share's server is the URL's host; leave it in the path and the
	// terminal opens nothing. Only Windows keeps the leading "//" through
	// filepath.Abs, so only Windows reaches that branch.
	if runtime.GOOS == "windows" {
		unc, want := `\\server\share\dd state\run 1.report.txt`, "file://server/share/dd%20state/run%201.report.txt"
		if got := fileURL(unc); got != want {
			t.Fatalf("fileURL(%q) = %q, want %q", unc, got, want)
		}
	}
}

// Terminals underline a hyperlink, so one stretched over the padding wrapText
// adds draws a rule across the whole frame instead of marking the text.
func TestFinishedFrameLinksGlyphsOnly(t *testing.T) {
	for _, w := range []int{40, 70, 100} {
		m := finishedModel()
		m.width = w
		frame, _ := m.viewRunning()
		for n, ln := range strings.Split(frame, "\n") {
			for _, seg := range strings.Split(ln, "\x1b]8;;")[1:] {
				if strings.HasPrefix(seg, "\x1b\\") {
					continue // the closer, so what trails it sits outside the link
				}
				i := strings.Index(seg, "\x1b\\")
				if i < 0 {
					continue
				}
				if body := seg[i+2:]; body != strings.TrimSpace(body) {
					t.Errorf("width %d, line %d: link covers padding: %q", w, n, body)
				}
			}
		}
	}
}

// A --no-tui run is usually redirected somewhere, and a log file has no use for
// escape sequences.
func TestPlainPathLinkPipedStaysBare(t *testing.T) {
	path := filepath.Join("state", "run-20260731-120000.report.txt")
	got := plainPathLink(path)
	if got != path {
		t.Fatalf("plainPathLink(%q) = %q", path, got)
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("escape bytes reached piped output: %q", got)
	}
}

func TestHomeAndConfigureRender(t *testing.T) {
	m := testModel()
	if out := m.viewHome(); !strings.Contains(out, "discord-delete") || !strings.Contains(out, "messages") {
		t.Fatalf("home view missing content:\n%s", out)
	}
	if out := m.viewConfigure(); !strings.Contains(out, "Settings") || !strings.Contains(out, "Order") {
		t.Fatalf("configure view missing content:\n%s", out)
	}
	if out := m.viewChannels(); !strings.Contains(out, "Servers") {
		t.Fatalf("channels view missing content:\n%s", out)
	}
}

// The Plan panel has to cost the phases that will actually run: reactions pace
// on their own floor, and a two-phase run pays for both.
func TestPlanPanelCoversEnabledPhases(t *testing.T) {
	m := testModel()
	m.cfg.reactionDelay = 0.3
	m.setReactions([]Reaction{
		{ChannelID: "r1", MessageID: "1200000000000000001", EmojiName: "👍",
			Snowflake: 1200000000000000001, HasSnow: true},
		{ChannelID: "r1", MessageID: "1200000000000000002", EmojiName: "🔥",
			Snowflake: 1200000000000000002, HasSnow: true},
		{ChannelID: "r2", MessageID: "1200000000000000003", EmojiName: "🥒",
			Snowflake: 1200000000000000003, HasSnow: true},
	}, nil, PackageCapabilities{HasMessages: true, HasReactions: true}, "")

	msgETA := estimate(m.jobs, m.cfg.workers, time.Duration(m.cfg.delay*float64(time.Second)), m.cfg.maxRPS)
	reactETA := estimate(m.reactJobs, m.cfg.workers, time.Duration(m.cfg.reactionDelay*float64(time.Second)), m.cfg.maxRPS)
	if msgETA == 0 || reactETA == 0 || msgETA == reactETA {
		t.Fatalf("precondition: want two distinct non-zero phase estimates, got %v and %v", msgETA, reactETA)
	}

	m.cfg.delMessages, m.cfg.delReactions = true, false
	if got := m.previewETA(); got != msgETA {
		t.Errorf("messages only: ETA = %v, want %v", got, msgETA)
	}
	if body := stripEscapes(m.planBody()); !strings.Contains(body, "msg/s") || !strings.Contains(body, "~1.1s / delete") {
		t.Errorf("messages only: plan panel reads\n%s", body)
	}

	m.cfg.delMessages, m.cfg.delReactions = false, true
	if got := m.previewETA(); got != reactETA {
		t.Errorf("reactions only: ETA = %v, want %v", got, reactETA)
	}
	if body := stripEscapes(m.planBody()); !strings.Contains(body, "react/s") || !strings.Contains(body, "~0.3s / delete") {
		t.Errorf("reactions only: plan panel reads\n%s", body)
	}

	m.cfg.delMessages, m.cfg.delReactions = true, true
	if got := m.previewETA(); got != msgETA+reactETA {
		t.Errorf("both phases: ETA = %v, want %v", got, msgETA+reactETA)
	}
	body := stripEscapes(m.planBody())
	for _, want := range []string{"messages", "reactions", "~1.1s each", "~0.3s each"} {
		if !strings.Contains(body, want) {
			t.Errorf("both phases: plan panel missing %q:\n%s", want, body)
		}
	}
}

// With enough channels the account-wide request cap binds before the
// per-channel floor, and est. time and throughput describe the same run:
// 40 deletions at the capped 2/s is 20s, not the floor-only 11s.
func TestPlanPanelETARespectsAccountCap(t *testing.T) {
	raws := make([]RawChannel, 8)
	sel := map[string]bool{}
	for i := range raws {
		id := fmt.Sprintf("c%d", i)
		msgs := make([]Message, 5)
		for k := range msgs {
			msgs[k] = newMessage(fmt.Sprintf("10000000000000%02d%02d", i, k), "hi")
		}
		raws[i] = RawChannel{ChannelID: id, Label: "#ch" + id, Messages: msgs}
		sel[id] = true
	}
	cfg := runConfig{order: "oldest", workers: 4, delay: 1.1, maxRPS: 2, delMessages: true}
	m := newAppModel(raws, cfg, sel, "package.zip")
	if got := m.previewETA(); got != 20*time.Second {
		t.Fatalf("capped ETA = %v, want 20s", got)
	}
	body := stripEscapes(m.planBody())
	for _, want := range []string{"est. time    20s", "~2.0 msg/s"} {
		if !strings.Contains(body, want) {
			t.Errorf("plan panel missing %q:\n%s", want, body)
		}
	}
}

// Pause wins over both an estimate and a stall.
func TestEtaStrStates(t *testing.T) {
	live := Snapshot{Total: 100, Processed: 50, Deleted: 50, ETA: 90 * time.Second}
	if got := etaStr(live, false); got != "1m30s" {
		t.Errorf("live estimate = %q, want 1m30s", got)
	}
	if got := etaStr(live, true); got != "paused" {
		t.Errorf("paused with an estimate = %q, want paused", got)
	}
	stalled := Snapshot{Total: 100, Processed: 50, Deleted: 50}
	if got := etaStr(stalled, false); got != "stalled" {
		t.Errorf("empty window with prior deletions = %q, want stalled", got)
	}
	if got := etaStr(Snapshot{Total: 100}, false); got != "…" {
		t.Errorf("no deletions yet = %q, want …", got)
	}
	if got := etaStr(Snapshot{Total: 100, Processed: 100, Deleted: 100, Finished: true}, true); got != "done" {
		t.Errorf("finished = %q, want done", got)
	}
}
