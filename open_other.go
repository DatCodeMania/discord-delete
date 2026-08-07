//go:build !windows

package main

import (
	"cmp"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

// hasDesktop reports whether a graphical session is reachable. xdg-open gates its
// MIME dispatch on these two variables and falls through to a web browser without
// them. macOS sets neither even with a desktop present.
func hasDesktop() bool {
	if runtime.GOOS == "darwin" {
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// terminalEditor builds the command for $VISUAL or $EDITOR, nil if neither is set.
// Both are shell fragments by convention, so the spec goes to sh; binding the path
// through "$@" passes it as data that sh never parses as code.
func terminalEditor(path string) *exec.Cmd {
	// Trimmed before the pick, or an all-whitespace VISUAL would count as set and
	// shadow a perfectly good EDITOR.
	spec := cmp.Or(strings.TrimSpace(os.Getenv("VISUAL")), strings.TrimSpace(os.Getenv("EDITOR")))
	if spec == "" {
		return nil
	}
	return exec.Command("/bin/sh", "-c", spec+` "$@"`, "sh", path)
}

// openDetached hands path to the desktop's handler for its type.
func openDetached(path string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, path)
	// xdg-open runs a Terminal=true handler on the caller's tty by design, so the
	// child gets its own session to keep it away from the TUI.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// xdg-open does not fork the handler off, so the child outlives this call and
	// is reaped off the update path.
	go func() { _ = cmd.Wait() }()
	return nil
}
