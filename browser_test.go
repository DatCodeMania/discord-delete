package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/network"
)

func TestUserAuthHeader(t *testing.T) {
	cases := []struct {
		name string
		h    network.Headers
		want string
	}{
		{"titlecase user token", network.Headers{"Authorization": "user.token.value"}, "user.token.value"},
		{"lowercase key", network.Headers{"authorization": "tok"}, "tok"},
		{"bot token skipped", network.Headers{"Authorization": "Bot abcdef"}, ""},
		{"bearer skipped", network.Headers{"Authorization": "Bearer xyz"}, ""},
		{"empty value", network.Headers{"Authorization": ""}, ""},
		{"non-string value", network.Headers{"Authorization": 1234}, ""},
		{"no auth header", network.Headers{"Accept": "application/json"}, ""},
	}
	for _, c := range cases {
		if got := userAuthHeader(c.h); got != c.want {
			t.Errorf("%s: userAuthHeader = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFindChromeHonorsOverride(t *testing.T) {
	t.Setenv("DISCORD_DELETE_CHROME", "/opt/custom/chrome")
	if got := findChrome(); got != "/opt/custom/chrome" {
		t.Fatalf("findChrome should return the override verbatim, got %q", got)
	}
}

func TestLaunchErrorNeverReportsMissingBrowser(t *testing.T) {
	// chromedp hands back Chrome's stderr, so a missing shared library surfaces
	// as "No such file or directory" from a browser that was found and launched.
	crashed := errors.New("chrome failed to start:\n" +
		"/opt/google/chrome/chrome: error while loading shared libraries: " +
		"libgbm.so.1: cannot open shared object file: No such file or directory")
	got := launchError("/opt/google/chrome/chrome", crashed)
	if errors.Is(got, errNoChrome) {
		t.Fatalf("a launch failure must not map to errNoChrome, got %v", got)
	}
	if !errors.Is(got, crashed) {
		t.Fatalf("launchError should wrap the underlying error, got %v", got)
	}
	if !strings.Contains(got.Error(), "/opt/google/chrome/chrome") {
		t.Fatalf("launchError should name the binary it tried, got %v", got)
	}
	if !strings.Contains(got.Error(), "libgbm.so.1") {
		t.Fatalf("launchError should keep chromedp's message, got %v", got)
	}
}

func TestSandboxKind(t *testing.T) {
	cases := map[string]string{
		"/snap/bin/chromium":                                     "snap",
		"/var/lib/flatpak/exports/bin/com.google.Chrome":         "Flatpak",
		"/home/me/.local/share/flatpak/exports/bin/com.brave.Br": "Flatpak",
		"/usr/bin/google-chrome-stable":                          "",
		"/opt/brave.com/brave/brave":                             "",
		// Not a snap: the prefix must be the /snap root, not any path saying snap.
		"/usr/bin/snapshot-tool": "",
	}
	for path, want := range cases {
		if got := sandboxKind(path); got != want {
			t.Errorf("sandboxKind(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestPickBrowserPrefersUnsandboxed(t *testing.T) {
	// Ubuntu's snap Chromium sits on PATH, so it is found first while a deb
	// install shows up later in the search.
	got := pickBrowser([]string{"/snap/bin/chromium", "/opt/google/chrome/chrome"})
	if want := "/opt/google/chrome/chrome"; got != want {
		t.Errorf("pickBrowser = %q, want %q", got, want)
	}
	// A sandboxed browser is still better than none, so it stays a fallback.
	if got := pickBrowser([]string{"/snap/bin/chromium"}); got != "/snap/bin/chromium" {
		t.Errorf("lone sandboxed browser should be used, got %q", got)
	}
	if got := pickBrowser(nil); got != "" {
		t.Errorf("no candidates should yield %q, got %q", "", got)
	}
}

func TestLaunchErrorHintsAtSandbox(t *testing.T) {
	boom := errors.New("chrome failed to start:")
	got := launchError("/snap/bin/chromium", boom).Error()
	for _, want := range []string{"snap", os.TempDir(), "DISCORD_DELETE_CHROME"} {
		if !strings.Contains(got, want) {
			t.Errorf("sandbox hint missing %q, got %v", want, got)
		}
	}
	// An ordinary install gets no hint to wade through.
	if plain := launchError("/opt/google/chrome/chrome", boom).Error(); strings.Contains(plain, "DISCORD_DELETE_CHROME") {
		t.Errorf("unsandboxed failure should not mention the override, got %v", plain)
	}
}

func TestBrowserErrLine(t *testing.T) {
	multi := errors.New("chrome failed to start:\n  [ERROR:bus.cc(399)] noise\nlibgbm.so.1: missing")
	got := browserErrLine(multi)
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("status line must be single-line, got %q", got)
	}
	if want := "chrome failed to start: [ERROR:bus.cc(399)] noise libgbm.so.1: missing"; got != want {
		t.Fatalf("browserErrLine = %q, want %q", got, want)
	}

	long := errors.New(strings.Repeat("x", 300) + " the-fatal-bit")
	got = browserErrLine(long)
	if r := []rune(got); len(r) != 200 {
		t.Fatalf("over-long message should be capped at 200 runes, got %d", len(r))
	}
	if !strings.HasSuffix(got, "the-fatal-bit") {
		t.Fatalf("truncation should keep the tail, got %q", got)
	}
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("truncated message should be marked, got %q", got)
	}
}

func TestChromeInstallPathsNonEmpty(t *testing.T) {
	// Platform lists are static, so this only checks that the current OS yields
	// candidates. Windows depends on env, so its absence just skips the test.
	if paths := chromeInstallPaths(); len(paths) == 0 {
		t.Skip("no static candidates for this OS/env")
	}
}

func TestChromeInstallPathsLinux(t *testing.T) {
	got := strings.Join(chromeInstallPathsFor("linux"), "\n")
	// These are the entries a PATH lookup misses: the real binaries under /opt
	// when no /usr/bin wrapper is installed, and Arch's brave, which Debian and
	// Ubuntu instead name brave-browser.
	for _, want := range []string{
		"/opt/google/chrome/chrome",
		"/opt/brave.com/brave/brave",
		"/opt/microsoft/msedge/msedge",
		"/opt/vivaldi/vivaldi",
		"/usr/bin/brave",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("linux candidates missing %s", want)
		}
	}
}

func TestChromeInstallPathsDarwinCoversUserApplications(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	paths := chromeInstallPathsFor("darwin")

	sys := filepath.Join("/Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome")
	user := filepath.Join(home, "Applications", "Brave Browser.app", "Contents", "MacOS", "Brave Browser")
	for _, want := range []string{sys, user} {
		if !slices.Contains(paths, want) {
			t.Errorf("darwin candidates missing %s", want)
		}
	}
	// A system-wide install is the common case, so it must be tried first.
	if slices.Index(paths, sys) > slices.Index(paths, user) {
		t.Error("/Applications should be searched before ~/Applications")
	}
}

func TestChromeInstallPathsWindowsUsesEnvBases(t *testing.T) {
	// Both sides compose paths with filepath.Join, so what this pins is that every
	// base is paired with every vendor path, not how separators render. Edge ships
	// under the x86 tree even when it is a 64-bit build, and a per-user Chrome or
	// Brave lands in LocalAppData, so no base can be dropped.
	bases := []string{`C:\PF`, `C:\PFx86`, `C:\Users\me\AppData\Local`}
	t.Setenv("ProgramFiles", bases[0])
	t.Setenv("ProgramFiles(x86)", bases[1])
	t.Setenv("LocalAppData", bases[2])

	rels := []string{
		`Google\Chrome\Application\chrome.exe`,
		`Chromium\Application\chrome.exe`,
		`Microsoft\Edge\Application\msedge.exe`,
		`BraveSoftware\Brave-Browser\Application\brave.exe`,
		`Vivaldi\Application\vivaldi.exe`,
	}
	paths := chromeInstallPathsFor("windows")
	for _, base := range bases {
		for _, rel := range rels {
			if want := filepath.Join(base, rel); !slices.Contains(paths, want) {
				t.Errorf("windows candidates missing %s", want)
			}
		}
	}
	if len(paths) != len(bases)*len(rels) {
		t.Errorf("got %d candidates, want %d", len(paths), len(bases)*len(rels))
	}
}

func TestChromeInstallPathsWindowsSkipsUnsetBases(t *testing.T) {
	t.Setenv("ProgramFiles", "")
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LocalAppData", "")
	if paths := chromeInstallPathsFor("windows"); len(paths) != 0 {
		t.Fatalf("no env bases should yield no candidates, got %v", paths)
	}
}
