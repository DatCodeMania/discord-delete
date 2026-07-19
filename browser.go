package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// errNoChrome is returned when no Chromium-family browser can be located.
var errNoChrome = errors.New("no Chrome/Chromium/Edge/Brave found")

// browserSigninMsg is delivered to the TUI when the browser sign-in flow ends.
type browserSigninMsg struct {
	token string
	err   error
}

// findChrome locates a Chromium-family browser across platforms, or returns ""
// if none is found. DISCORD_DELETE_CHROME overrides the search with an explicit
// path (useful for an unusual install or a flatpak wrapper).
func findChrome() string {
	if p := strings.TrimSpace(os.Getenv("DISCORD_DELETE_CHROME")); p != "" {
		return p
	}
	// Names commonly on PATH (Linux/BSD, and Homebrew symlinks on macOS).
	for _, n := range []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"brave-browser", "microsoft-edge", "chrome",
	} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	for _, p := range chromeInstallPaths() {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func chromeInstallPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	case "windows":
		var out []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			base := os.Getenv(env)
			if base == "" {
				continue
			}
			out = append(out,
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(base, `Chromium\Application\chrome.exe`),
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `BraveSoftware\Brave-Browser\Application\brave.exe`),
			)
		}
		return out
	default: // linux, *bsd
		return []string{
			"/usr/bin/google-chrome", "/usr/bin/google-chrome-stable",
			"/usr/bin/chromium", "/usr/bin/chromium-browser",
			"/snap/bin/chromium", "/usr/bin/brave-browser", "/usr/bin/microsoft-edge",
		}
	}
}

// captureTokenFromBrowser launches a visible browser at discord.com/login, waits
// for the user to sign in, and lifts the Authorization header off the first
// authenticated Discord API request. The password is typed into the real
// browser, never through this process, and a throwaway profile keeps the
// user's real browser profile untouched. Honors ctx for cancellation.
func captureTokenFromBrowser(ctx context.Context) (string, error) {
	chromePath := findChrome()
	if chromePath == "" {
		return "", errNoChrome
	}

	profile, err := os.MkdirTemp("", "discord-delete-signin-*")
	if err != nil {
		return "", fmt.Errorf("create temp profile: %w", err)
	}
	defer os.RemoveAll(profile)

	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", false), // the user needs to see and use the window
		chromedp.UserDataDir(profile),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	tokenCh := make(chan string, 1)
	chromedp.ListenTarget(browserCtx, func(ev any) {
		e, ok := ev.(*network.EventRequestWillBeSent)
		if !ok || e.Request == nil {
			return
		}
		if !strings.Contains(e.Request.URL, "discord.com/api") {
			return
		}
		if auth := userAuthHeader(e.Request.Headers); auth != "" {
			select {
			case tokenCh <- auth:
			default:
			}
		}
	})

	if err := chromedp.Run(browserCtx,
		network.Enable(),
		chromedp.Navigate("https://discord.com/login"),
	); err != nil {
		return "", launchError(err)
	}

	select {
	case tok := <-tokenCh:
		return tok, nil
	case <-browserCtx.Done():
		// The user closed the window, or our context was cancelled.
		return "", context.Canceled
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// userAuthHeader returns a plausible Discord *user* token from request headers
// (case-insensitive key), skipping bot ("Bot …") and OAuth ("Bearer …") values.
func userAuthHeader(h network.Headers) string {
	for k, v := range h {
		if !strings.EqualFold(k, "authorization") {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		low := strings.ToLower(s)
		if s == "" || strings.HasPrefix(low, "bot ") || strings.HasPrefix(low, "bearer ") {
			continue
		}
		return s
	}
	return ""
}

// launchError maps a browser-launch failure to a friendlier error, collapsing
// "binary missing" cases to errNoChrome.
func launchError(err error) error {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file") {
		return errNoChrome
	}
	return fmt.Errorf("couldn't open a browser window (is a desktop session available?): %w", err)
}
