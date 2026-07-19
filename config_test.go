package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigRoundTripExcludesToken(t *testing.T) {
	raws := []RawChannel{
		{ChannelID: "1", Label: "#a"},
		{ChannelID: "2", Label: "#b"},
		{ChannelID: "3", Label: "#c"},
	}
	cfg := runConfig{
		order: "newest", content: "oops", afterDate: "2023-01-01", last: "30d",
		typeSel: map[string]bool{"image": true, "voice": true},
		workers: 8, delay: 2.0, jitter: 0.1, maxRPS: 10,
		token: "SUPER-SECRET-TOKEN", execute: true, ntfy: "my-topic", remember: true,
	}
	selected := map[string]bool{"1": true, "2": false, "3": true}
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := saveConfig(path, cfg, selected, raws, reactPersist{}); err != nil {
		t.Fatal(err)
	}

	// The token must NEVER appear in the file.
	raw, _ := os.ReadFile(path)
	data := string(raw)
	if strings.Contains(data, "SUPER-SECRET-TOKEN") || strings.Contains(strings.ToLower(data), "token") {
		t.Fatalf("token leaked into config file:\n%s", data)
	}

	// Round-trip onto a fresh default config with no flags set.
	loaded, ok := loadConfig(path)
	if !ok {
		t.Fatal("expected to load the config")
	}
	got := defaultRunConfig()
	sel := map[string]bool{}
	applyPersisted(&got, sel, loaded, map[string]bool{}, raws)

	if got.order != "newest" || got.content != "oops" || got.last != "30d" ||
		got.workers != 8 || got.delay != 2.0 || got.jitter != 0.1 || got.maxRPS != 10 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if !got.typeSel["image"] || !got.typeSel["voice"] || got.typeSel["video"] {
		t.Fatalf("types not restored: %v", got.typeSel)
	}
	if got.token != "" { // token is never persisted, so it stays empty here
		t.Fatalf("token must not be restored from disk, got %q", got.token)
	}
	if got.ntfy != "my-topic" {
		t.Fatalf("ntfy should round-trip, got %q", got.ntfy)
	}
	if !got.remember {
		t.Fatal("remember preference should round-trip")
	}
	if !sel["1"] || sel["2"] || !sel["3"] {
		t.Fatalf("channel selection not restored: %v", sel)
	}
}

func TestParseNotifyEvery(t *testing.T) {
	ok := map[string]time.Duration{
		"":     0,
		"off":  0,
		"none": 0,
		"0":    0,
		"30m":  30 * time.Minute,
		"1h":   time.Hour,
		"90m":  90 * time.Minute,
		" 1h ": time.Hour,
	}
	for in, want := range ok {
		got, err := parseNotifyEvery(in)
		if err != nil {
			t.Errorf("parseNotifyEvery(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseNotifyEvery(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"30", "10s", "banana", "-5m"} {
		if _, err := parseNotifyEvery(bad); err == nil {
			t.Errorf("parseNotifyEvery(%q) should have errored", bad)
		}
	}
}

func TestFmtNotifyEvery(t *testing.T) {
	cases := map[time.Duration]string{
		0:                "off",
		30 * time.Minute: "30m",
		time.Hour:        "1h",
		90 * time.Minute: "1h30m",
		2 * time.Hour:    "2h",
		45 * time.Minute: "45m",
	}
	for d, want := range cases {
		if got := fmtNotifyEvery(d); got != want {
			t.Errorf("fmtNotifyEvery(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestNotifyEveryRoundTrips(t *testing.T) {
	raws := []RawChannel{{ChannelID: "1", Label: "#a"}}
	cfg := defaultRunConfig()
	cfg.notifyEvery = 90 * time.Minute
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := saveConfig(path, cfg, map[string]bool{"1": true}, raws, reactPersist{}); err != nil {
		t.Fatal(err)
	}
	loaded, ok := loadConfig(path)
	if !ok {
		t.Fatal("expected to load config")
	}
	got := defaultRunConfig()
	applyPersisted(&got, map[string]bool{}, loaded, map[string]bool{}, raws)
	if got.notifyEvery != 90*time.Minute {
		t.Fatalf("notifyEvery should round-trip, got %v", got.notifyEvery)
	}
}

func TestExplicitFlagsWinOverSavedConfig(t *testing.T) {
	raws := []RawChannel{{ChannelID: "1", Label: "#a"}}
	saved := persistedConfig{Order: "newest", Workers: 8, Delay: 2.0, Content: "saved", AllChannels: true}

	cfg := defaultRunConfig()
	cfg.order = "oldest" // as if from the flag default
	cfg.content = "flagval"
	cfg.workers = 4
	sel := map[string]bool{"1": true}
	// User explicitly passed --order and --content; --workers was NOT passed.
	applyPersisted(&cfg, sel, saved, map[string]bool{"order": true, "content": true}, raws)

	if cfg.order != "oldest" {
		t.Fatalf("explicit --order should win, got %q", cfg.order)
	}
	if cfg.content != "flagval" {
		t.Fatalf("explicit --content should win, got %q", cfg.content)
	}
	if cfg.workers != 8 {
		t.Fatalf("unset --workers should take the saved value 8, got %d", cfg.workers)
	}
}

func TestDeleteTargetsRoundTrip(t *testing.T) {
	raws := []RawChannel{{ChannelID: "m1", Label: "#a"}}
	reactRaws := []RawChannel{{ChannelID: "r1"}, {ChannelID: "r2"}}
	cfg := defaultRunConfig()
	cfg.delMessages = true
	cfg.delReactions = true
	cfg.reactionsFirst = true
	cfg.reactionDelay = 0.5
	react := reactPersist{
		caps:     PackageCapabilities{HasMessages: true, HasReactions: true},
		selected: map[string]bool{"r1": true, "r2": false},
		raws:     reactRaws,
	}
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := saveConfig(path, cfg, map[string]bool{"m1": true}, raws, react); err != nil {
		t.Fatal(err)
	}
	loaded, ok := loadConfig(path)
	if !ok {
		t.Fatal("expected to load config")
	}

	got := defaultRunConfig()
	got.delReactions = false // as if freshly defaulted before restore
	applyPersisted(&got, map[string]bool{}, loaded, map[string]bool{}, raws)
	if !got.delMessages || !got.delReactions || !got.reactionsFirst || got.reactionDelay != 0.5 {
		t.Fatalf("delete targets not restored: %+v", got)
	}

	rsel := map[string]bool{"r1": true, "r2": true}
	applyPersistedReactChannels(rsel, reactRaws, loaded, map[string]bool{})
	if !rsel["r1"] || rsel["r2"] {
		t.Fatalf("reaction channel selection not restored: %v", rsel)
	}
}

// TestOldConfigKeepsDeleteDefaults guards the backward-compat case: a config
// written before the dual-mode feature has no del_* fields, so restoring it must
// leave the capability-derived defaults alone, not force the targets off.
func TestOldConfigKeepsDeleteDefaults(t *testing.T) {
	raws := []RawChannel{{ChannelID: "m1"}}
	saved := persistedConfig{Order: "newest", AllChannels: true} // no del_* fields
	cfg := defaultRunConfig()                                    // delMessages defaults on
	cfg.delReactions = true                                      // as if caps enabled it
	applyPersisted(&cfg, map[string]bool{}, saved, map[string]bool{}, raws)
	if !cfg.delMessages {
		t.Fatal("absent del_messages must not disable message deletion")
	}
	if !cfg.delReactions {
		t.Fatal("absent del_reactions must not disable reaction deletion")
	}
}

func TestResetDefaultsKeepsToken(t *testing.T) {
	m := demoModel()
	m.cfg.order = "newest"
	m.cfg.content = "oops"
	m.cfg.workers = 32
	m.cfg.execute = true
	m.cfg.token = "keepme"
	m.cfg.typeSel = map[string]bool{"image": true}
	m.selected["1"] = false

	m.resetDefaults()

	if m.cfg.order != defOrder || m.cfg.content != "" || m.cfg.workers != defWorkers ||
		m.cfg.delay != defDelay || m.cfg.execute || len(m.cfg.typeSel) != 0 {
		t.Fatalf("reset did not restore defaults: %+v", m.cfg)
	}
	if m.cfg.token != "keepme" {
		t.Fatalf("reset must keep the token, got %q", m.cfg.token)
	}
	for id, on := range m.selected {
		if !on {
			t.Fatalf("reset should re-select all channels, %s is off", id)
		}
	}
}
