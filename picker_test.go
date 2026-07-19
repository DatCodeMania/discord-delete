package main

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCleanPickerPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct{ in, want string }{
		{"  /tmp/pkg.zip  ", "/tmp/pkg.zip"},
		{`"/tmp/my pkg.zip"`, "/tmp/my pkg.zip"},
		{"'/tmp/pkg.zip'", "/tmp/pkg.zip"},
		{`/tmp/my\ pkg.zip`, "/tmp/my pkg.zip"},
		{"~/pkg.zip", filepath.Join(home, "pkg.zip")},
		{"", ""},
	}
	for _, c := range cases {
		if got := cleanPickerPath(c.in); got != c.want {
			t.Errorf("cleanPickerPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPathHint(t *testing.T) {
	dir := t.TempDir()
	zip := filepath.Join(dir, "package.zip")
	writeFile(t, zip, "not really a zip")

	if _, ok := pathHint(filepath.Join(dir, "nope.zip")); ok {
		t.Error("missing path should not be a good sign")
	}
	if _, ok := pathHint(dir); !ok {
		t.Error("existing folder should be a good sign")
	}
	if msg, ok := pathHint(zip); !ok || msg == "" {
		t.Errorf("existing .zip should be a good sign, got %q ok=%v", msg, ok)
	}
}

// TestPickerLoadsPackage drives the model the way Bubble Tea would: type a path,
// press Enter, deliver the load result, and confirm it quits with the loaded channels.
func TestPickerLoadsPackage(t *testing.T) {
	root := t.TempDir()
	chDir := filepath.Join(root, "messages", "c123456")
	if err := os.MkdirAll(chDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(chDir, "messages.json"), `[{"ID":"111"},{"ID":"222"}]`)

	m := newPickerModel()
	if m.View() == "" {
		t.Fatal("initial picker view should render something")
	}
	m.input.SetValue(root)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.loading || cmd == nil {
		t.Fatal("Enter on a valid path should start a load")
	}
	msg := cmd() // run the load command synchronously
	loaded, ok := msg.(pkgLoadedMsg)
	if !ok {
		t.Fatalf("load command should return pkgLoadedMsg, got %T", msg)
	}
	if loaded.err != nil {
		t.Fatalf("valid package should load, got %v", loaded.err)
	}

	_, quitCmd := m.Update(loaded)
	if m.chosen != root || m.pkg == nil || len(m.pkg.Raws) == 0 {
		t.Fatalf("successful load should set chosen=%q with channels, got %q / %+v", root, m.chosen, m.pkg)
	}
	if m.quit {
		t.Error("a successful load is not a user quit")
	}
	if quitCmd == nil {
		t.Error("a successful load should quit the picker program")
	}
}

func TestPickerRejectsBadPackage(t *testing.T) {
	m := newPickerModel()
	m.input.SetValue(t.TempDir()) // empty dir: no messages

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should attempt a load")
	}
	m.Update(cmd())
	if m.chosen != "" {
		t.Error("an empty package must not be chosen")
	}
	if m.errMsg == "" {
		t.Error("a failed load should surface an error")
	}
	if m.loading {
		t.Error("loading should clear after the result")
	}
}
