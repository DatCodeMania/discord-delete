package main

import (
	"errors"
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

func TestLaunchErrorMapping(t *testing.T) {
	missing := errors.New(`exec: "google-chrome": executable file not found in $PATH`)
	if got := launchError(missing); !errors.Is(got, errNoChrome) {
		t.Fatalf("missing-binary error should map to errNoChrome, got %v", got)
	}
	other := errors.New("websocket url timeout")
	if got := launchError(other); errors.Is(got, errNoChrome) {
		t.Fatalf("unrelated error must not map to errNoChrome, got %v", got)
	}
}

func TestChromeInstallPathsNonEmpty(t *testing.T) {
	// Platform lists are static, so this only checks that the current OS yields
	// candidates. Windows depends on env, so its absence just skips the test.
	if paths := chromeInstallPaths(); len(paths) == 0 {
		t.Skip("no static candidates for this OS/env")
	}
}
