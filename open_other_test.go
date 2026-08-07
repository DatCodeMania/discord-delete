//go:build !windows

package main

import (
	"runtime"
	"testing"
)

func TestHasDesktop(t *testing.T) {
	// macOS reaches its handler through `open`, which needs no display variable.
	if runtime.GOOS == "darwin" {
		t.Setenv("DISPLAY", "")
		t.Setenv("WAYLAND_DISPLAY", "")
		if !hasDesktop() {
			t.Fatal("darwin has a desktop whether or not the X variables are set")
		}
		return
	}

	for _, tc := range []struct {
		name, display, wayland string
		want                   bool
	}{
		{"neither", "", "", false},
		{"x11", ":0", "", true},
		{"wayland", "", "wayland-0", true},
		{"both", ":0", "wayland-0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DISPLAY", tc.display)
			t.Setenv("WAYLAND_DISPLAY", tc.wayland)
			if got := hasDesktop(); got != tc.want {
				t.Errorf("hasDesktop() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTerminalEditorPicksSpec(t *testing.T) {
	for _, tc := range []struct {
		name, visual, editor, want string
	}{
		{"visual wins", "vis", "ed", "vis"},
		{"editor when visual unset", "", "ed", "ed"},
		{"blank visual falls through", "   ", "ed", "ed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VISUAL", tc.visual)
			t.Setenv("EDITOR", tc.editor)
			cmd := terminalEditor("report.txt")
			if cmd == nil {
				t.Fatal("want a command, got nil")
			}
			if got := cmd.Args[2]; got != tc.want+` "$@"` {
				t.Errorf("script = %q, want %q", got, tc.want+` "$@"`)
			}
		})
	}

	for _, tc := range []struct{ name, visual, editor string }{
		{"neither set", "", ""},
		{"both blank", "  ", "\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VISUAL", tc.visual)
			t.Setenv("EDITOR", tc.editor)
			if cmd := terminalEditor("report.txt"); cmd != nil {
				t.Errorf("want nil without an editor, got %v", cmd.Args)
			}
		})
	}
}

// $EDITOR is a shell fragment, so it has to reach sh unsplit, and the path has to
// arrive as data sh never parses. Both halves are what keep a metacharacter in the
// report path from running as code.
func TestTerminalEditorKeepsPathOutOfTheScript(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "code -w")

	const nasty = `/tmp/dd; rm -rf ~/$(whoami) & echo 'pwn'.txt`
	cmd := terminalEditor(nasty)
	if cmd == nil {
		t.Fatal("want a command, got nil")
	}

	want := []string{"/bin/sh", "-c", `code -w "$@"`, "sh", nasty}
	if len(cmd.Args) != len(want) {
		t.Fatalf("Args = %q, want %q", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Errorf("Args[%d] = %q, want %q", i, cmd.Args[i], want[i])
		}
	}
}
