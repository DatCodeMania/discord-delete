//go:build windows

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// setAppPath writes an App Paths default value that is removed when the test
// ends. HKCU is the only root a test can write without elevation, and the exe
// names are fake so a run cannot shadow a real browser's entry.
func setAppPath(t *testing.T, exe, value string, expand bool) {
	t.Helper()
	path := appPathsKey + exe
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		t.Skipf("cannot write HKCU %s: %v", path, err)
	}
	if expand {
		err = k.SetExpandStringValue("", value)
	} else {
		err = k.SetStringValue("", value)
	}
	if cerr := k.Close(); err == nil {
		err = cerr
	}
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, path) })
	if err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func fakeExe(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("MZ"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	return p
}

func TestAppPathValueReadsDefaultValue(t *testing.T) {
	want := fakeExe(t, t.TempDir(), "dd-plain.exe")
	setAppPath(t, "dd-plain.exe", want, false)
	if got := appPathValue(registry.CURRENT_USER, registry.QUERY_VALUE, "dd-plain.exe"); got != want {
		t.Fatalf("appPathValue = %q, want %q", got, want)
	}
}

func TestAppPathValueTrimsQuotesAndSpace(t *testing.T) {
	// Installers quote the path so a space in Program Files survives ShellExecute.
	want := fakeExe(t, t.TempDir(), "dd-quoted.exe")
	setAppPath(t, "dd-quoted.exe", `  "`+want+`"  `, false)
	if got := appPathValue(registry.CURRENT_USER, registry.QUERY_VALUE, "dd-quoted.exe"); got != want {
		t.Fatalf("appPathValue = %q, want %q", got, want)
	}
}

func TestAppPathValueExpandsEnvVars(t *testing.T) {
	// A REG_EXPAND_SZ entry stores %LOCALAPPDATA%-style references verbatim, so
	// skipping the expansion leaves a path that stats as missing.
	dir := t.TempDir()
	want := fakeExe(t, dir, "dd-expand.exe")
	t.Setenv("DISCORD_DELETE_TEST_APPDIR", dir)
	setAppPath(t, "dd-expand.exe", `%DISCORD_DELETE_TEST_APPDIR%\dd-expand.exe`, true)
	if got := appPathValue(registry.CURRENT_USER, registry.QUERY_VALUE, "dd-expand.exe"); got != want {
		t.Fatalf("appPathValue = %q, want %q", got, want)
	}
}

func TestAppPathValueRejectsUnusableEntries(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		exe     string
		value   string
		present bool
	}{
		{"no key at all", "dd-absent.exe", "", false},
		{"empty value", "dd-empty.exe", "", true},
		{"quotes around nothing", "dd-hollow.exe", `""`, true},
		{"uninstalled browser", "dd-stale.exe", filepath.Join(dir, "gone.exe"), true},
		{"value names a directory", "dd-dir.exe", dir, true},
	}
	for _, c := range cases {
		if c.present {
			setAppPath(t, c.exe, c.value, false)
		}
		if got := appPathValue(registry.CURRENT_USER, registry.QUERY_VALUE, c.exe); got != "" {
			t.Errorf("%s: appPathValue = %q, want empty", c.name, got)
		}
	}
}

func TestAppPathProbesCoverHKCUAndBothWow64Views(t *testing.T) {
	// Dropping any one probe silently loses a whole class of install, which a
	// passing browser search on the developer's own machine will not reveal.
	var hkcu bool
	var hklmViews []uint32
	for _, p := range appPathProbes {
		switch p.root {
		case registry.CURRENT_USER:
			hkcu = true
		case registry.LOCAL_MACHINE:
			hklmViews = append(hklmViews, p.access&(registry.WOW64_64KEY|registry.WOW64_32KEY))
		}
	}
	if !hkcu {
		t.Error("HKCU is not probed, so per-user installs are invisible")
	}
	for _, want := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
		if !slices.Contains(hklmViews, want) {
			t.Errorf("HKLM view %#x is not probed, got %#x", want, hklmViews)
		}
	}
}

func TestChromeFromRegistryReturnsAnExistingFile(t *testing.T) {
	// A runner may have no Chromium-family browser installed, so an empty result
	// is legitimate and only the non-empty case has anything to assert.
	got := chromeFromRegistry()
	if got == "" {
		return
	}
	fi, err := os.Stat(got)
	if err != nil {
		t.Fatalf("chromeFromRegistry returned %q, which does not exist: %v", got, err)
	}
	if fi.IsDir() {
		t.Fatalf("chromeFromRegistry returned directory %q", got)
	}
}
