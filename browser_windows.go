//go:build windows

package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// appPathsKey is where a Windows installer records an executable's full path so
// ShellExecute can find it without touching PATH. It covers installs that a
// fixed list of default directories misses.
const appPathsKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`

// appPathProbes are the registry views to search, in order. Chrome and Brave
// default to per-user installs, which register under HKCU. HKLM\SOFTWARE is
// WOW64-redirected, so an entry written by a 32-bit installer sits in a view
// this 64-bit process does not see by default, and both are read explicitly.
var appPathProbes = []struct {
	root   registry.Key
	access uint32
}{
	{registry.CURRENT_USER, registry.QUERY_VALUE},
	{registry.LOCAL_MACHINE, registry.QUERY_VALUE | registry.WOW64_64KEY},
	{registry.LOCAL_MACHINE, registry.QUERY_VALUE | registry.WOW64_32KEY},
}

// chromeFromRegistry resolves a Chromium-family browser through App Paths.
func chromeFromRegistry() string {
	for _, exe := range []string{
		"chrome.exe", "msedge.exe", "brave.exe", "chromium.exe",
		"vivaldi.exe", "opera.exe",
	} {
		for _, probe := range appPathProbes {
			if p := appPathValue(probe.root, probe.access, exe); p != "" {
				return p
			}
		}
	}
	return ""
}

// appPathValue reads one App Paths default value. It returns "" unless the value
// names a file that exists, so a stale entry left by an uninstalled browser
// cannot win over a working one further down the search.
func appPathValue(root registry.Key, access uint32, exe string) string {
	k, err := registry.OpenKey(root, appPathsKey+exe, access)
	if err != nil {
		return ""
	}
	defer func() { _ = k.Close() }()
	val, typ, err := k.GetStringValue("")
	if err != nil {
		return ""
	}
	if typ == registry.EXPAND_SZ {
		if expanded, eerr := registry.ExpandString(val); eerr == nil {
			val = expanded
		}
	}
	val = strings.Trim(strings.TrimSpace(val), `"`)
	if val == "" {
		return ""
	}
	if fi, serr := os.Stat(val); serr != nil || fi.IsDir() {
		return ""
	}
	return val
}
