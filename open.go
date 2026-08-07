package main

import (
	"errors"

	tea "github.com/charmbracelet/bubbletea"
)

// errNoEditor is returned when neither VISUAL nor EDITOR names a program.
var errNoEditor = errors.New("no VISUAL or EDITOR set")

// openReport opens the report in the desktop's handler where there is a desktop,
// otherwise the terminal editor. A var so tests can take a click without a
// handler opening on someone's desktop.
var openReport = func(path string) tea.Cmd {
	if hasDesktop() {
		return func() tea.Msg { return openDoneMsg{path, openDetached(path)} }
	}
	ed := terminalEditor(path)
	if ed == nil {
		return func() tea.Msg { return openDoneMsg{path, errNoEditor} }
	}
	return tea.ExecProcess(ed, func(err error) tea.Msg { return openDoneMsg{path, err} })
}
