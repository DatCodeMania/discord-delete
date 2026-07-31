package main

import (
	"os/exec"
	"runtime"
)

// openFile hands path to the desktop's handler for its type. A var so tests can
// take a click without a text editor opening on someone's desktop.
var openFile = func(path string) error {
	switch runtime.GOOS {
	case "windows":
		// Not cmd's start builtin: that needs a console window of its own.
		return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}
