//go:build windows

package main

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// hasDesktop is unconditionally true on Windows, where ShellExecute resolves a
// handler without consulting a display variable.
func hasDesktop() bool { return true }

// terminalEditor is unreachable on Windows, where hasDesktop keeps openReport on
// the desktop branch.
func terminalEditor(string) *exec.Cmd { return nil }

// openDetached asks the shell to open path with its registered handler. Going
// through ShellExecute rather than cmd.exe leaves & and ^ in the path needing no
// escaping, and spawns no child to reap.
func openDetached(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, nil, p, nil, nil, windows.SW_SHOWNORMAL)
}
