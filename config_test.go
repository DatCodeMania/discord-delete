package main

import (
	"os"
	"path/filepath"
	"strconv"
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

func TestFmtNotifyEveryKeepsSeconds(t *testing.T) {
	cases := map[string]string{
		"1m":       "1m",
		"5m":       "5m",
		"90s":      "1m30s",
		"2m30s":    "2m30s",
		"90m":      "1h30m",
		"1h":       "1h",
		"1h30m15s": "1h30m15s",
		"1m0.5s":   "1m0.5s",
	}
	for in, want := range cases {
		d, err := parseNotifyEvery(in)
		if err != nil {
			t.Errorf("parseNotifyEvery(%q) errored: %v", in, err)
			continue
		}
		got := fmtNotifyEvery(d)
		if got != want {
			t.Errorf("fmtNotifyEvery(%q) = %q, want %q", in, got, want)
			continue
		}
		back, err := parseNotifyEvery(got)
		if err != nil {
			t.Errorf("parseNotifyEvery(%q) errored: %v", got, err)
			continue
		}
		if back != d {
			t.Errorf("%q round-tripped to %v, want %v", in, back, d)
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

func TestNoMessagesOutranksSavedReactionsOff(t *testing.T) {
	raws := []RawChannel{{ChannelID: "m1"}}
	caps := PackageCapabilities{HasMessages: true, HasReactions: true}
	setFlags := map[string]bool{"no-messages": true}

	cfg := defaultRunConfig()
	cfg.delMessages, cfg.delReactions = resolveDeleteTargets(caps, false, true)
	markImpliedFlags(setFlags, cfg, true)

	on, off := true, false
	saved := persistedConfig{Order: "oldest", AllChannels: true, DelMessages: &on, DelReactions: &off}
	applyPersisted(&cfg, map[string]bool{}, saved, setFlags, raws)

	if cfg.delMessages {
		t.Fatal("--no-messages must keep message deletion off")
	}
	if !cfg.delReactions {
		t.Fatal("saved delete_reactions:false must not empty a --no-messages run")
	}
}

// A bound on the command line silences every saved field spelling the same end;
// resolveBounds would otherwise intersect the two.
func TestExplicitBoundsOutrankSavedBounds(t *testing.T) {
	raws := []RawChannel{{ChannelID: "1"}}
	saved := persistedConfig{
		Order: "oldest", AllChannels: true,
		AfterDate: "2024-01-01", BeforeDate: "2024-06-01", Last: "7d",
	}

	for _, tc := range []struct {
		flag    string
		set     func(*runConfig)
		silence []string
	}{
		{"after", func(c *runConfig) { c.afterSnow = "1100000000000000000" }, []string{"after-date", "last"}},
		{"after-date", func(c *runConfig) { c.afterDate = "2023-01-01" }, []string{"last"}},
		{"last", func(c *runConfig) { c.last = "30d" }, []string{"after-date"}},
		{"before", func(c *runConfig) { c.beforeSnow = "1200000000000000000" }, []string{"before-date"}},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			cfg := defaultRunConfig()
			tc.set(&cfg)
			setFlags := map[string]bool{tc.flag: true}
			markImpliedFlags(setFlags, cfg, false)
			applyPersisted(&cfg, map[string]bool{"1": true}, saved, setFlags, raws)

			got := map[string]string{"after-date": cfg.afterDate, "before-date": cfg.beforeDate, "last": cfg.last}
			for _, field := range tc.silence {
				if got[field] != "" {
					t.Fatalf("--%s must silence the saved %s, got %q", tc.flag, field, got[field])
				}
			}
		})
	}
}

// The user-visible half: a saved window under an explicit snowflake range
// collapses it and the run dies on "empty date range".
func TestSavedWindowCannotEmptySnowflakeRange(t *testing.T) {
	now := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	wantAfter := timeToSnowflake(now.AddDate(-2, 0, 0))

	cfg := defaultRunConfig()
	cfg.afterSnow = strconv.FormatUint(wantAfter, 10)
	cfg.beforeSnow = strconv.FormatUint(timeToSnowflake(now.AddDate(-1, 0, 0)), 10)
	setFlags := map[string]bool{"after": true, "before": true}
	markImpliedFlags(setFlags, cfg, false)

	saved := persistedConfig{Order: "oldest", AllChannels: true, Last: "7d"}
	applyPersisted(&cfg, map[string]bool{"1": true}, saved, setFlags, []RawChannel{{ChannelID: "1"}})

	tb, err := resolveBounds(cfg.afterSnow, cfg.beforeSnow, cfg.afterDate, cfg.beforeDate, cfg.last, now)
	if err != nil {
		t.Fatalf("explicit snowflake range must resolve: %v", err)
	}
	if tb.AfterID != wantAfter {
		t.Fatalf("after bound: got %d want %d", tb.AfterID, wantAfter)
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

// TestPlainSnapshotKeepsFlagOnlyConfig pins the plain fallback to the CLI flags:
// applyPersisted overlays the saved channel selection in place, and the type
// screen toggles types in place, so the snapshot must see neither.
func TestPlainSnapshotKeepsFlagOnlyConfig(t *testing.T) {
	raws := []RawChannel{{ChannelID: "1"}, {ChannelID: "2"}}
	cfg := defaultRunConfig()
	cfg.typeSel = map[string]bool{"image": true}
	sel := map[string]bool{"1": true, "2": false}

	plain := plainRun{cfg: cfg, sel: sel}.clone()

	saved := persistedConfig{Order: "newest", AllChannels: true}
	// An explicit --type keeps the flag-built map, and the type screen then
	// toggles it in place.
	applyPersisted(&cfg, sel, saved, map[string]bool{"type": true}, raws)
	cfg.typeSel["voice"] = true

	if !sel["2"] {
		t.Fatal("applyPersisted should restore the saved selection")
	}
	if plain.sel["2"] {
		t.Fatalf("snapshot took the saved selection: %v", plain.sel)
	}
	if !plain.cfg.typeSel["image"] || plain.cfg.typeSel["voice"] {
		t.Fatalf("snapshot took a later type edit: %v", plain.cfg.typeSel)
	}
}
