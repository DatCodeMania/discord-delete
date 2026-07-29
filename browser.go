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
var errNoChrome = errors.New("no Chromium-based browser found")

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
	return pickBrowser(chromeCandidates())
}

// chromeCandidates returns every browser it can find, in preference order.
func chromeCandidates() []string {
	var out []string
	// Names commonly on PATH, which in practice means Linux and BSD: macOS casks
	// and Windows installers do not add one.
	for _, n := range []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"brave-browser", "brave", "microsoft-edge", "microsoft-edge-stable",
		"vivaldi", "vivaldi-stable", "opera", "chrome",
	} {
		if p, err := exec.LookPath(n); err == nil {
			out = append(out, p)
		}
	}
	if p := chromeFromRegistry(); p != "" {
		out = append(out, p)
	}
	for _, p := range chromeInstallPaths() {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// pickBrowser takes the first candidate installed outside a sandbox, falling
// back to a sandboxed one only when nothing else is present. On Ubuntu the snap
// Chromium sits on PATH and would otherwise beat a working deb install.
func pickBrowser(candidates []string) string {
	var sandboxed string
	for _, p := range candidates {
		if sandboxKind(p) == "" {
			return p
		}
		if sandboxed == "" {
			sandboxed = p
		}
	}
	return sandboxed
}

// sandboxKind names the packaging of a browser that runs sandboxed, or returns
// "" for an ordinary install. Snap and Flatpak both give the app a private /tmp,
// so the sign-in profile this process creates there is invisible to it.
func sandboxKind(path string) string {
	switch {
	case strings.HasPrefix(path, "/snap/"):
		return "snap"
	case strings.Contains(path, "/flatpak/exports/bin/"):
		return "Flatpak"
	}
	return ""
}

func chromeInstallPaths() []string {
	home, _ := os.UserHomeDir()
	return chromeInstallPathsFor(runtime.GOOS, home)
}

// chromeInstallPathsFor lists a platform's default install locations, in
// preference order. The home directory is a parameter rather than a lookup
// because os.UserHomeDir switches on the host's GOOS, which would make this
// depend on where it runs as well as on goos.
func chromeInstallPathsFor(goos, home string) []string {
	switch goos {
	case "darwin":
		var out []string
		// A drag-install by a user without admin rights lands in ~/Applications,
		// not /Applications.
		dirs := []string{"/Applications"}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "Applications"))
		}
		for _, base := range dirs {
			for _, bundle := range []string{
				"Google Chrome", "Chromium", "Microsoft Edge",
				"Brave Browser", "Vivaldi", "Arc", "Opera",
			} {
				out = append(out, filepath.Join(base, bundle+".app", "Contents", "MacOS", bundle))
			}
		}
		return out
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
				filepath.Join(base, `Vivaldi\Application\vivaldi.exe`),
			)
		}
		return out
	default: // linux, *bsd
		return []string{
			"/usr/bin/google-chrome", "/usr/bin/google-chrome-stable",
			"/opt/google/chrome/chrome",
			"/usr/bin/chromium", "/usr/bin/chromium-browser", "/snap/bin/chromium",
			"/usr/bin/brave-browser", "/usr/bin/brave", "/opt/brave.com/brave/brave",
			"/usr/bin/microsoft-edge", "/usr/bin/microsoft-edge-stable",
			"/opt/microsoft/msedge/msedge",
			"/usr/bin/vivaldi", "/usr/bin/vivaldi-stable", "/opt/vivaldi/vivaldi",
			"/usr/bin/opera", "/usr/local/bin/chrome", "/usr/local/bin/chromium",
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
	// Keep enable-automation as it blocks password saving.
	opts = append(opts,
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", false),
		chromedp.Flag("hide-scrollbars", false),
		chromedp.Flag("mute-audio", false),
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
		return "", launchError(chromePath, err)
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

// launchError reports a failed launch, naming the binary that was tried: a bad
// DISCORD_DELETE_CHROME and a broken install need different fixes. findChrome
// has already resolved a path, so "no such file" in the wrapped message is
// Chrome's stderr about a shared library, not an absent browser.
func launchError(path string, err error) error {
	// The hint goes last so browserErrLine's tail-first truncation keeps it.
	if kind := sandboxKind(path); kind != "" {
		return fmt.Errorf("browser at %s failed to start: %w; %s builds cannot read the sign-in profile in %s, so point DISCORD_DELETE_CHROME at a browser installed outside a sandbox",
			path, err, kind, os.TempDir())
	}
	return fmt.Errorf("browser at %s failed to start: %w", path, err)
}

// browserErrLine condenses err into a single status line. chromedp includes all
// of Chrome's stderr, which is multi-line and front-loaded with unrelated
// warnings, so truncation keeps the tail.
func browserErrLine(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	const max = 200
	if r := []rune(s); len(r) > max {
		return "…" + string(r[len(r)-max+1:])
	}
	return s
}
