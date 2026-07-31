package main

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

	out := m.viewRunning()
	if !strings.Contains(out, "discord-delete") || !strings.Contains(out, "deleted") {
		t.Fatalf("running view missing expected content:\n%s", out)
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
	prev := openFile
	openFile = func(p string) error { got = p; return err }
	t.Cleanup(func() { openFile = prev })
	return &got
}

func TestRunningViewLinksReportPath(t *testing.T) {
	m := finishedModel()

	out := m.viewRunning()
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
	lines := strings.Split(m.viewRunning(), "\n") // rendering records the hit-box
	btn := m.reportHit

	// A hit-box that disagrees with the rendered frame sends clicks to nowhere.
	if btn.y >= len(lines) || !strings.Contains(lines[btn.y], btnOpenReport) {
		t.Fatalf("row %d does not hold the button", btn.y)
	}
	if got := btn.x1 - btn.x0 + 1; got != lipgloss.Width(btnOpenReport) {
		t.Errorf("button spans %d columns, want %d", got, lipgloss.Width(btnOpenReport))
	}

	got := stubOpen(t, nil)
	click := func(x, y int) {
		*got = ""
		m.updateRunning(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
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
	m.updateRunning(tea.MouseMsg{X: btn.x0, Y: btn.y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m.updateRunning(tea.MouseMsg{X: btn.x0, Y: btn.y, Action: tea.MouseActionPress, Button: tea.MouseButtonRight})
	if *got != "" {
		t.Errorf("only a left press should open; got %q", *got)
	}
}

// The keyboard fallback, for terminals that report no mouse.
func TestFinishedFrameOpensOnKey(t *testing.T) {
	m := finishedModel()
	got := stubOpen(t, nil)

	m.updateRunning(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if *got != m.reportPath {
		t.Errorf("o opened %q, want %q", *got, m.reportPath)
	}

	// Mid-run there is no report yet, so the key does nothing.
	live := testModel()
	live.stats, live.screen = NewStats(4, 1), scRunning
	*got = ""
	live.updateRunning(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if *got != "" {
		t.Errorf("o during a run opened %q", *got)
	}
}

// Bubble Tea paints only the last height lines, so the row has to move with them.
func TestReportButtonRowShiftsWhenFrameOverflows(t *testing.T) {
	m := finishedModel()
	full := strings.Split(m.viewRunning(), "\n")
	base := m.reportHit.y

	m.height = len(full) - 3
	m.viewRunning()
	visible := full[len(full)-m.height:] // the slice Bubble Tea actually paints

	got := m.reportHit.y
	if got != base-3 {
		t.Fatalf("row = %d, want %d once 3 lines scroll off the top", got, base-3)
	}
	if got < 0 || got >= len(visible) || !strings.Contains(visible[got], btnOpenReport) {
		t.Errorf("shifted row %d does not hold the button", got)
	}
}

func TestFinishedFrameShowsOpenFailure(t *testing.T) {
	m := finishedModel()
	m.viewRunning()
	stubOpen(t, errors.New("no xdg-open"))

	m.updateRunning(tea.MouseMsg{
		X: m.reportHit.x0, Y: m.reportHit.y,
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
	})
	if out := m.viewRunning(); !strings.Contains(out, "no xdg-open") {
		t.Errorf("open failure not surfaced:\n%s", out)
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
