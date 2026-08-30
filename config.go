package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Default config values, shared by the CLI flag defaults and the TUI's
// "Reset to defaults" action so the two never drift.
const (
	defOrder         = "oldest"
	defWorkers       = 4
	defDelay         = 1.1
	defJitter        = 0.4
	defMaxRPS        = 25.0
	defNotifyEvery   = 30 * time.Minute
	defReactionDelay = 0.3 // reactions tolerate a faster per-channel floor than messages
)

func defaultRunConfig() runConfig {
	return runConfig{
		order:         defOrder,
		workers:       defWorkers,
		delay:         defDelay,
		jitter:        defJitter,
		maxRPS:        defMaxRPS,
		notifyEvery:   defNotifyEvery,
		delMessages:   true,
		reactionDelay: defReactionDelay,
	}
}

// parseNotifyEvery reads a progress-notify interval: a Go duration like "30m" or
// "1h", or "off"/"0"/"none"/"" to disable periodic notifications. Anything under
// a minute is rejected so a typo can't spam ntfy.
func parseNotifyEvery(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "off" || s == "0" || s == "none" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid interval %q: use a duration like 30m or 1h, or 'off'", s)
	}
	if d < time.Minute {
		return 0, fmt.Errorf("interval must be at least 1m (got %s)", s)
	}
	return d, nil
}

// fmtNotifyEvery renders an interval compactly ("30m", "1h", "1h30m", "1m30s",
// "off"). The output is fed back through parseNotifyEvery on the next launch, so
// it has to carry the full precision of what the parser accepts.
func fmtNotifyEvery(d time.Duration) string {
	if d <= 0 {
		return "off"
	}
	if d%time.Minute != 0 {
		return d.String() // an h/m split would drop the odd seconds
	}
	mins := int(d.Minutes())
	h, m := mins/60, mins%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// persistedConfig is the per-package, non-sensitive configuration saved to disk.
// The token is deliberately not a field here: credentials are never written to
// disk. Execute mode isn't saved either, so a launch always starts in safe dry
// run.
type persistedConfig struct {
	Order            string   `json:"order"`
	Content          string   `json:"content"`
	AfterDate        string   `json:"after_date"`
	BeforeDate       string   `json:"before_date"`
	Last             string   `json:"last"`
	Types            []string `json:"types,omitempty"`
	Workers          int      `json:"workers"`
	Delay            float64  `json:"delay"`
	Jitter           float64  `json:"jitter"`
	MaxRPS           float64  `json:"max_rps"`
	Ntfy             string   `json:"ntfy,omitempty"`
	NotifyEvery      string   `json:"notify_every,omitempty"`
	Remember         bool     `json:"remember,omitempty"`
	AllChannels      bool     `json:"all_channels"`
	SelectedChannels []string `json:"selected_channels,omitempty"`

	// Dual-mode delete targets. Pointers so a config written before this feature
	// (fields absent) is left to the capability-derived defaults rather than being
	// forced off. Reaction fields are written only for packages that have reactions.
	DelMessages      *bool    `json:"del_messages,omitempty"`
	DelReactions     *bool    `json:"del_reactions,omitempty"`
	ReactionsFirst   *bool    `json:"reactions_first,omitempty"`
	ReactionDelay    *float64 `json:"reaction_delay,omitempty"`
	ReactAllChannels bool     `json:"react_all_channels,omitempty"`
	ReactChannels    []string `json:"react_channels,omitempty"`
}

// reactPersist carries the reaction-selection inputs saveConfig needs beyond the
// runConfig. The zero value (no capability) writes no reaction fields, which is
// what message-only packages and tests pass.
type reactPersist struct {
	caps     PackageCapabilities
	selected map[string]bool
	raws     []RawChannel
}

// configPath is the per-package config file, keyed the same way as the resume
// log and living beside it.
func configPath(owner PackageOwner, pkgPath string) string {
	return filepath.Join(progressDir(), progressKey(owner, pkgPath)+".config.json")
}

func loadConfig(path string) (persistedConfig, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return persistedConfig{}, false
	}
	var p persistedConfig
	if json.Unmarshal(data, &p) != nil {
		return persistedConfig{}, false
	}
	return p, true
}

// saveConfig writes the non-sensitive config for a package. It never writes the
// token. Written 0600 via a temp file + rename so a crash can't leave a
// half-written file.
func saveConfig(path string, cfg runConfig, selected map[string]bool, raws []RawChannel, react reactPersist) error {
	data, err := json.MarshalIndent(toPersisted(cfg, selected, raws, react), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func toPersisted(cfg runConfig, selected map[string]bool, raws []RawChannel, react reactPersist) persistedConfig {
	p := persistedConfig{
		Order:       cfg.order,
		Content:     cfg.content,
		AfterDate:   cfg.afterDate,
		BeforeDate:  cfg.beforeDate,
		Last:        cfg.last,
		Workers:     cfg.workers,
		Delay:       cfg.delay,
		Jitter:      cfg.jitter,
		MaxRPS:      cfg.maxRPS,
		Ntfy:        cfg.ntfy,
		NotifyEvery: fmtNotifyEvery(cfg.notifyEvery),
		Remember:    cfg.remember,
	}
	for _, o := range typeOptions {
		if cfg.typeSel[o.id] {
			p.Types = append(p.Types, o.id)
		}
	}
	if all, ids := selectionList(raws, selected); all {
		p.AllChannels = true
	} else {
		p.SelectedChannels = ids
	}

	// Delete-target fields, only for capabilities the package actually has, so a
	// message-only config stays free of meaningless reaction keys.
	if react.caps.HasMessages {
		b := cfg.delMessages
		p.DelMessages = &b
	}
	if react.caps.HasReactions {
		b := cfg.delReactions
		p.DelReactions = &b
		d := cfg.reactionDelay
		p.ReactionDelay = &d
		if react.caps.HasMessages { // run order only matters when both run
			rf := cfg.reactionsFirst
			p.ReactionsFirst = &rf
		}
		if all, ids := selectionList(react.raws, react.selected); all {
			p.ReactAllChannels = true
		} else {
			p.ReactChannels = ids
		}
	}
	return p
}

// selectionList reports whether every channel is selected and, when not, the
// sorted ids of those that are.
func selectionList(raws []RawChannel, selected map[string]bool) (all bool, ids []string) {
	all = true
	for _, rc := range raws {
		if selected[rc.ChannelID] {
			ids = append(ids, rc.ChannelID)
		} else {
			all = false
		}
	}
	if all {
		return true, nil
	}
	sort.Strings(ids)
	return false, ids
}

// resolveDeleteTargets picks the phases a run deletes. Reactions also run
// whenever messages are skipped, so --no-messages alone means reactions only.
func resolveDeleteTargets(caps PackageCapabilities, reactions, noMessages bool) (delMessages, delReactions bool) {
	delMessages = caps.HasMessages && !noMessages
	delReactions = caps.HasReactions && (reactions || !delMessages)
	return delMessages, delReactions
}

// markImpliedFlags sets the guards for fields a CLI value already decides under
// another flag's name, since applyPersisted checks one name per field and would
// otherwise layer the saved value onto the explicit one. Keyed on the value, not
// on flag.Visit: a flag passed empty sets nothing and so speaks for nothing.
func markImpliedFlags(setFlags map[string]bool, cfg runConfig, noMessages bool) {
	for _, imp := range []struct {
		set     bool
		implied []string
	}{
		// The ends stay independent: --before says nothing about where a run starts.
		{cfg.afterSnow != "", []string{"after-date", "last"}},
		{cfg.afterDate != "", []string{"last"}},
		{cfg.last != "", []string{"after-date"}},
		{cfg.beforeSnow != "", []string{"before-date"}},
		{noMessages, []string{"reactions"}},
	} {
		if !imp.set {
			continue
		}
		for _, name := range imp.implied {
			setFlags[name] = true
		}
	}
}

// applyPersisted overlays a saved config onto cfg/sel, but only for fields whose
// CLI flag was not explicitly passed (explicit flags always win). Values are
// validated and clamped so a hand-edited or stale file can't produce a bad
// config.
func applyPersisted(cfg *runConfig, sel map[string]bool, p persistedConfig, setFlags map[string]bool, raws []RawChannel) {
	if !setFlags["order"] && (p.Order == "oldest" || p.Order == "newest") {
		cfg.order = p.Order
	}
	if !setFlags["content"] {
		cfg.content = p.Content
	}
	if !setFlags["after-date"] {
		cfg.afterDate = p.AfterDate
	}
	if !setFlags["before-date"] {
		cfg.beforeDate = p.BeforeDate
	}
	if !setFlags["last"] {
		cfg.last = p.Last
	}
	if !setFlags["type"] {
		cfg.typeSel = map[string]bool{}
		for _, id := range p.Types {
			if typeIDMask(id) != 0 {
				cfg.typeSel[id] = true
			}
		}
	}
	if !setFlags["workers"] && p.Workers > 0 {
		cfg.workers = clampInt(p.Workers, 1, 64)
	}
	if !setFlags["delay"] && p.Delay >= 0 {
		cfg.delay = p.Delay
	}
	if !setFlags["jitter"] && p.Jitter >= 0 {
		cfg.jitter = p.Jitter
	}
	if !setFlags["max-rps"] && p.MaxRPS > 0 {
		cfg.maxRPS = clampFloat(p.MaxRPS, 1, 49)
	}
	if !setFlags["ntfy"] {
		cfg.ntfy = p.Ntfy
	}
	if !setFlags["notify-every"] && p.NotifyEvery != "" {
		if d, err := parseNotifyEvery(p.NotifyEvery); err == nil {
			cfg.notifyEvery = d
		}
	}
	if !setFlags["remember"] {
		cfg.remember = p.Remember
	}
	// Delete targets: restore each unless its flag was explicitly passed. Absent
	// (nil) means a pre-feature config, so the capability-derived default stands.
	if !setFlags["no-messages"] && p.DelMessages != nil {
		cfg.delMessages = *p.DelMessages
	}
	if !setFlags["reactions"] && p.DelReactions != nil {
		cfg.delReactions = *p.DelReactions
	}
	if !setFlags["run-order"] && p.ReactionsFirst != nil {
		cfg.reactionsFirst = *p.ReactionsFirst
	}
	if !setFlags["reaction-delay"] && p.ReactionDelay != nil && *p.ReactionDelay >= 0 {
		cfg.reactionDelay = *p.ReactionDelay
	}
	// Channel selection only when neither --guild nor --channel was passed.
	if !setFlags["guild"] && !setFlags["channel"] {
		applyChannelSelection(sel, raws, p.AllChannels, p.SelectedChannels)
	}
}

// applyChannelSelection overlays a saved channel selection onto sel: all-selected,
// an explicit subset, or (when neither was recorded) leaves sel untouched.
func applyChannelSelection(sel map[string]bool, raws []RawChannel, all bool, ids []string) {
	switch {
	case all:
		for _, rc := range raws {
			sel[rc.ChannelID] = true
		}
	case len(ids) > 0:
		want := map[string]bool{}
		for _, id := range ids {
			want[id] = true
		}
		for _, rc := range raws {
			sel[rc.ChannelID] = want[rc.ChannelID]
		}
	}
}

// applyPersistedReactChannels restores the saved reaction-channel selection onto
// reactSelected, unless --reaction-channel was passed (that flag wins).
func applyPersistedReactChannels(reactSelected map[string]bool, reactRaws []RawChannel, p persistedConfig, setFlags map[string]bool) {
	if setFlags["reaction-channel"] {
		return
	}
	applyChannelSelection(reactSelected, reactRaws, p.ReactAllChannels, p.ReactChannels)
}

// persistedFlagNames are the flag names the applyPersisted guards check.
var persistedFlagNames = []string{
	"order", "content", "after-date", "before-date", "last", "type",
	"workers", "delay", "jitter", "max-rps", "ntfy", "notify-every",
	"no-messages", "reactions", "run-order", "reaction-delay",
	"guild", "channel", "reaction-channel",
}

// flagFieldEqual compares the runConfig field a flag controls. Selection flags
// live outside runConfig and report true; the caller compares those maps.
func flagFieldEqual(name string, a, b runConfig) bool {
	switch name {
	case "order":
		return a.order == b.order
	case "content":
		return a.content == b.content
	case "after-date":
		return a.afterDate == b.afterDate
	case "before-date":
		return a.beforeDate == b.beforeDate
	case "last":
		return a.last == b.last
	case "type":
		return maps.Equal(a.typeSel, b.typeSel)
	case "workers":
		return a.workers == b.workers
	case "delay":
		return a.delay == b.delay
	case "jitter":
		return a.jitter == b.jitter
	case "max-rps":
		return a.maxRPS == b.maxRPS
	case "ntfy":
		return a.ntfy == b.ntfy
	case "notify-every":
		return a.notifyEvery == b.notifyEvery
	case "no-messages":
		return a.delMessages == b.delMessages
	case "reactions":
		return a.delReactions == b.delReactions
	case "run-order":
		return a.reactionsFirst == b.reactionsFirst
	case "reaction-delay":
		return a.reactionDelay == b.reactionDelay
	}
	return true
}

// startState is what the TUI opened with. The maps are cloned because the TUI
// edits them in place.
type startState struct {
	cfg      runConfig
	sel      map[string]bool
	reactSel map[string]bool
}

func snapshotStart(cfg runConfig, sel, reactSel map[string]bool) startState {
	cfg.typeSel = maps.Clone(cfg.typeSel)
	return startState{cfg: cfg, sel: maps.Clone(sel), reactSel: maps.Clone(reactSel)}
}

// dropUneditedFlags reverts every field that holds its value only because a
// flag was passed, so a flag configures one run and only a TUI edit is saved.
// A field edited back to the flag's own value cannot be told from an untouched
// one and reverts too.
func dropUneditedFlags(cfg *runConfig, sel, reactSelected map[string]bool, start startState,
	setFlags map[string]bool, saved persistedConfig, haveSaved bool,
	caps PackageCapabilities, raws, reactRaws []RawChannel) {
	// The baseline this run would have started from with no flags at all.
	base := defaultRunConfig()
	base.delMessages, base.delReactions = resolveDeleteTargets(caps, false, false)
	baseSel := initialSelection(raws, nil, nil)
	baseReact := initialSelection(reactRaws, nil, nil)
	if haveSaved {
		applyPersisted(&base, baseSel, saved, map[string]bool{}, raws)
		applyPersistedReactChannels(baseReact, reactRaws, saved, map[string]bool{})
	}
	basePersisted := toPersisted(base, baseSel, raws,
		reactPersist{caps: caps, selected: baseReact, raws: reactRaws})
	basePersisted.Remember = cfg.remember // no flag, so the overlay must not move it

	// applyPersisted skips names marked set, so only the unmarked take the baseline.
	keep := map[string]bool{}
	for _, n := range persistedFlagNames {
		keep[n] = true
	}
	for n := range setFlags {
		if flagFieldEqual(n, *cfg, start.cfg) {
			delete(keep, n)
		}
	}
	// Equality against the snapshot stands in for "untouched". --guild and
	// --channel narrow one map, so they revert only together.
	if (setFlags["guild"] || setFlags["channel"]) && maps.Equal(sel, start.sel) {
		delete(keep, "guild")
		delete(keep, "channel")
	} else {
		keep["guild"], keep["channel"] = true, true
	}
	if setFlags["reaction-channel"] && maps.Equal(reactSelected, start.reactSel) {
		delete(keep, "reaction-channel")
	} else {
		keep["reaction-channel"] = true
	}
	applyPersisted(cfg, sel, basePersisted, keep, raws)
	applyPersistedReactChannels(reactSelected, reactRaws, basePersisted, keep)
}
