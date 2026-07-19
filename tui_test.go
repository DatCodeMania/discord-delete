package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
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
