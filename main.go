package main

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
)

// buildVersion is stamped at release-build time via -ldflags -X; "dev" for a
// plain `go build`.
var buildVersion = "dev"

func main() {
	var (
		pkgPath   = flag.String("package", "", "path to your Discord data package (.zip or extracted folder); omit to open guided setup")
		token     = flag.String("token", "", "user token")
		guildF    = flag.String("guild", "", "delete only these guild IDs")
		chanF     = flag.String("channel", "", "delete only these comma-separated channel IDs")
		contentF  = flag.String("content", "", "only delete messages whose text contains this substring (or a regex if wrapped in slashes: /pattern/ or /pattern/i)")
		afterF    = flag.String("after", "", "only messages after this message ID")
		beforeF   = flag.String("before", "", "only messages before this message ID")
		afterD    = flag.String("after-date", "", "only messages after this date (YYYY-MM-DD or RFC3339)")
		beforeD   = flag.String("before-date", "", "only messages before this date (YYYY-MM-DD or RFC3339)")
		lastF     = flag.String("last", "", "only messages within the last window: 7d, 2w, 3mo, 1y, day/week/month/year, or today (since local midnight)")
		typeF     = flag.String("type", "", "only these message types (comma-separated): text,media,image,video,audio,voice,file,link")
		orderF    = flag.String("order", defOrder, "deletion order per channel: oldest | newest")
		workers   = flag.Int("workers", defWorkers, "channels deleted concurrently")
		delay     = flag.Float64("delay", defDelay, "base seconds between deletes within one channel")
		jitter    = flag.Float64("jitter", defJitter, "+/- jitter fraction applied to --delay")
		maxRPS    = flag.Float64("max-rps", defMaxRPS, "hard account-wide request/sec ceiling (safety cap, <50)")
		noTUI     = flag.Bool("no-tui", false, "plain non-interactive run")
		execute   = flag.Bool("execute", false, "start in execute mode (real deletion) instead of dry run")
		ntfyF     = flag.String("ntfy", "", "ntfy topic (or full URL) to ping when the run finishes (or set DISCORD_DELETE_NTFY)")
		everyF    = flag.String("notify-every", "", "push a progress notification (with a pause button) this often during a run: 30m, 1h, or 'off' (default 30m; needs --ntfy)")
		reportF   = flag.String("report", "", "write the end-of-run report to this path (default: in user cache, alongside the resume log)")
		forgetF   = flag.Bool("forget-token", false, "delete any stored token for this package's account, then exit")
		rememberF = flag.Bool("remember", false, "save a validated token to your OS keyring for next time; --remember=false overrides a saved on without deleting the stored token")
		versionF  = flag.Bool("version", false, "print version and exit")

		reactionsF  = flag.Bool("reactions", false, "also remove your reactions (needs an Activity/reporting folder in the package)")
		noMessagesF = flag.Bool("no-messages", false, "skip message deletion (e.g. to only remove reactions)")
		runOrderF   = flag.String("run-order", "messages", "which phase runs first when doing both: messages | reactions")
		reactDelayF = flag.Float64("reaction-delay", defReactionDelay, "base seconds between reaction removals within one channel")
		reactChanF  = flag.String("reaction-channel", "", "comma-separated channel IDs to scope reaction removal (default: all)")
	)
	flag.Usage = usage
	flag.Parse()

	// Nothing takes a positional argument, and "--flag false" parses as a bare
	// flag plus a stray word, which would mean the opposite of what was typed.
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected argument %q. On/off flags take their value with an = sign, as in --remember=false.\n", flag.Arg(0))
		os.Exit(2)
	}

	if *versionF {
		fmt.Printf("discord-delete %s\n", buildVersion)
		return
	}

	// Start the update check in the background; each path below collects it.
	updateCh := startUpdateCheck()

	// Flags the user explicitly passed win over any saved config.
	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	order := strings.ToLower(strings.TrimSpace(*orderF))
	if order != "oldest" && order != "newest" {
		fmt.Fprintf(os.Stderr, "error: --order must be 'oldest' or 'newest', got %q\n", *orderF)
		os.Exit(2)
	}

	tok := *token
	if tok == "" {
		tok = os.Getenv("DISCORD_TOKEN")
	}

	ntfy := *ntfyF
	if ntfy == "" {
		ntfy = os.Getenv("DISCORD_DELETE_NTFY")
	}
	// An env-provided value should win over saved config, just like a flag.
	if ntfy != "" {
		setFlags["ntfy"] = true
	}

	notifyEvery := defNotifyEvery
	if setFlags["notify-every"] {
		d, perr := parseNotifyEvery(*everyF)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "error: --notify-every: %v\n", perr)
			os.Exit(2)
		}
		notifyEvery = d
	}

	typeSel, err := parseTypes(*typeF)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	// --forget-token only needs the account key (a light owner read), not the
	// full package parse (the Activity scan alone can be ~1GB).
	if *forgetF {
		if *pkgPath == "" {
			fmt.Fprintln(os.Stderr, "error: --forget-token needs --package to identify the account (tokens are stored per account).")
			os.Exit(2)
		}
		fOwner, _ := LoadPackageOwner(*pkgPath)
		if err := forgetToken(progressKey(fOwner, *pkgPath)); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Stored token (if any) for this account has been removed.")
		return
	}

	// Get the package. With one given, load it. With none, open the guided
	// first-run picker; when it can't open there's nothing to pick with, so
	// explain how to supply one and point at the guide.
	var pkg *LoadedPackage
	if *pkgPath == "" {
		if !canRunTUI(*noTUI, isatty.IsTerminal(os.Stdout.Fd())) {
			fmt.Fprintln(os.Stderr, noPackageHelp)
			os.Exit(2)
		}
		picked, pk, ok := runPicker()
		if !ok {
			return // user quit the picker without choosing a package
		}
		*pkgPath, pkg = picked, pk
	} else {
		// The TUI re-filters this in memory, so it's loaded just once.
		fmt.Printf("Reading data package: %s\n", *pkgPath)
		pkg, err = ReadPackage(*pkgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	raws := pkg.Raws
	// --no-messages leaves reactions as the only phase, so it needs the data too.
	if !pkg.Caps.HasReactions {
		switch {
		case *reactionsF:
			fmt.Fprintln(os.Stderr, "error: --reactions was passed, but this package has no Activity/reporting data (reactions).")
			os.Exit(2)
		case *noMessagesF:
			fmt.Fprintln(os.Stderr, "error: --no-messages was passed, but this package has no Activity/reporting data (reactions), so there would be nothing to delete.")
			os.Exit(2)
		}
	}

	delMessages, delReactions := resolveDeleteTargets(pkg.Caps, *reactionsF, *noMessagesF)

	cfg := runConfig{
		order:       order,
		content:     *contentF,
		afterSnow:   strings.TrimSpace(*afterF),
		beforeSnow:  strings.TrimSpace(*beforeF),
		afterDate:   strings.TrimSpace(*afterD),
		beforeDate:  strings.TrimSpace(*beforeD),
		last:        strings.TrimSpace(*lastF),
		typeSel:     typeSel,
		workers:     clampInt(*workers, 1, 64),
		delay:       *delay,
		jitter:      *jitter,
		maxRPS:      clampFloat(*maxRPS, 1, 49),
		token:       normalizeToken(tok),
		execute:     *execute,
		ntfy:        strings.TrimSpace(ntfy),
		notifyEvery: notifyEvery,
		remember:    *rememberF,

		delMessages:    delMessages,
		delReactions:   delReactions,
		reactionsFirst: strings.EqualFold(*runOrderF, "reactions"),
		reactionDelay:  *reactDelayF,
	}
	markImpliedFlags(setFlags, cfg, *noMessagesF)

	sel := initialSelection(raws, toSet(*guildF), toSet(*chanF))

	// Whose package is this? Used to refuse deleting with a different account.
	owner := pkg.Owner

	// Resume-log paths (the log itself is loaded once, on the path that runs).
	progPath := progressPath(owner, *pkgPath)
	reactProgPath := reactionProgressPath(owner, *pkgPath)
	stateKey := progressKey(owner, *pkgPath)
	reactSel := toSet(*reactChanF)

	// If no token was supplied, try a stored one for this account (OS keyring).
	loadedFromStore := false
	if cfg.token == "" {
		if stored, backend, ok := loadToken(stateKey); ok {
			cfg.token = stored
			loadedFromStore = true
			fmt.Printf("Loaded stored token from %s (will re-verify).\n", backend)
		}
	}

	plain := plainRun{
		pkg: pkg, cfg: cfg, sel: sel, reactSel: reactSel,
		pkgName: filepath.Base(*pkgPath), owner: owner,
		progPath: progPath, reactProgPath: reactProgPath,
		reportOverride: *reportF, stateKey: stateKey,
		tokenFromStore: loadedFromStore,
	}.clone()

	// The full TUI needs an interactive terminal. Without one (pipe/CI) or with
	// --no-tui, fall back to a plain, non-interactive run using the CLI flags
	// (saved config is a TUI convenience, so the plain path ignores it).
	if !canRunTUI(*noTUI, isatty.IsTerminal(os.Stdout.Fd())) {
		if notice := <-updateCh; notice != "" {
			fmt.Println("\033[2m" + notice + "\033[0m") // dim
		}
		runPlain(plain)
		return
	}

	// Restore this package's saved settings, but let any explicit flag win.
	cfgPath := configPath(owner, *pkgPath)
	saved, haveSaved := loadConfig(cfgPath)
	if haveSaved {
		applyPersisted(&cfg, sel, saved, setFlags, raws)
	}

	// Resume state (TUI path only; the headless path loads its own).
	done := loadProgressSet(progPath)

	model := newAppModel(raws, cfg, sel, filepath.Base(*pkgPath))
	model.updateCh = updateCh
	model.ownerID, model.ownerName, model.ownerHandle = owner.ID, owner.Name, owner.Handle
	model.progPath, model.done = progPath, done
	model.stateKey = stateKey
	model.tokenFromStore = loadedFromStore
	if loadedFromStore {
		model.savedToken = cfg.token // don't immediately re-save a just-loaded token
	}
	model.reportOverride = *reportF
	model.resumed = countInSet(raws, done)
	model.setReactions(pkg.Reactions, pkg.GuildNames, pkg.Caps, reactProgPath)
	// setReactions defaults the targets from capabilities; re-apply the flags.
	model.cfg.delMessages = cfg.delMessages
	model.cfg.delReactions = cfg.delReactions
	model.cfg.reactionsFirst = cfg.reactionsFirst
	model.cfg.reactionDelay = cfg.reactionDelay
	// Reaction channel scope: an explicit --reaction-channel wins; otherwise fall
	// back to the saved selection. This asks whether the flag named a channel,
	// while applyPersistedReactChannels asks whether it was passed at all, so
	// --reaction-channel "" keeps every channel and still drops the saved one.
	if len(reactSel) > 0 {
		for id := range model.reactSelected {
			model.reactSelected[id] = reactSel[id]
		}
	} else if haveSaved {
		applyPersistedReactChannels(model.reactSelected, model.reactRaws, saved, setFlags)
	}
	model.recompute() // reflect the resume set + saved config in the initial preview
	start := snapshotStart(model.cfg, model.selected, model.reactSelected)
	prog := tea.NewProgram(model, tea.WithAltScreen())
	final, err := prog.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tui error (%v); falling back to plain run\n", err)
		runPlain(plain)
		return
	}
	// Persist this package's settings for next time (never the token). Untouched
	// flag-set fields revert first: a flag configures the current run only.
	if fm, ok := final.(*appModel); ok {
		dropUneditedFlags(&fm.cfg, fm.selected, fm.reactSelected, start,
			setFlags, saved, haveSaved, fm.caps, raws, fm.reactRaws)
		_ = saveConfig(cfgPath, fm.cfg, fm.selected, raws,
			reactPersist{caps: fm.caps, selected: fm.reactSelected, raws: fm.reactRaws})
	}
}

// canRunTUI reports whether an interactive screen may open, gating both the
// guided picker and the full TUI. --no-tui asks for a non-interactive run, so it
// rules them out even on a terminal.
func canRunTUI(noTUI, terminal bool) bool {
	return !noTUI && terminal
}

// noPackageHelp is printed when no package is given and the guided picker cannot
// open (a bare double-click with no console, a pipe, cron, or --no-tui).
const noPackageHelp = `discord-delete needs a Discord data package to work with, and none was given.

Run it from a terminal with no options to open the guided setup:

  discord-delete

Or point straight at a package you already have:

  discord-delete --package path/to/package.zip

To get one: in Discord, open Settings, then Data & Privacy, and Request Data (select Messages and Activity).`

// runPicker opens the first-run welcome/picker screen and returns the chosen
// package path and its loaded channels. ok is false if the user quit without
// choosing.
func runPicker() (path string, pkg *LoadedPackage, ok bool) {
	final, err := tea.NewProgram(newPickerModel(), tea.WithAltScreen()).Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	pm, castOK := final.(*pickerModel)
	if !castOK || pm.quit || pm.chosen == "" || pm.pkg == nil {
		return "", nil, false
	}
	return pm.chosen, pm.pkg, true
}

// initialSelection marks every channel selected unless the flags narrow it.
func initialSelection(raws []RawChannel, guilds, channels map[string]bool) map[string]bool {
	sel := map[string]bool{}
	for _, rc := range raws {
		in := true
		if len(guilds) > 0 && !guilds[rc.GuildID] {
			in = false
		}
		if len(channels) > 0 && !channels[rc.ChannelID] {
			in = false
		}
		sel[rc.ChannelID] = in
	}
	return sel
}

// plainRun bundles everything the headless runner needs across both phases
// (message deletion, reaction removal).
type plainRun struct {
	pkg            *LoadedPackage
	cfg            runConfig
	sel            map[string]bool // message channel selection
	reactSel       map[string]bool // reaction channel selection (nil = all)
	pkgName        string
	owner          PackageOwner
	progPath       string // message resume log
	reactProgPath  string // reaction resume log
	reportOverride string
	stateKey       string
	tokenFromStore bool // cfg.token came from the OS keyring, not a flag or env
}

// clone returns a plainRun that shares no map with the caller. The plain run is
// the flag-only configuration, and both applyPersisted and the TUI rewrite the
// selection and type maps in place.
func (p plainRun) clone() plainRun {
	p.cfg.typeSel = maps.Clone(p.cfg.typeSel)
	p.sel = maps.Clone(p.sel)
	p.reactSel = maps.Clone(p.reactSel)
	return p
}

// plainPhase is one headless phase's plan: what to delete, the pacing floor, the
// resume log to append to, and the channel metadata for the undeletable rollup.
type plainPhase struct {
	kind     string
	jobs     []ChannelJob
	total    int
	floor    time.Duration
	progPath string
	meta     map[string]chanMeta
	resumed  int    // items already done in a prior run and skipped up front
	summary  string // one-line filter description for the preflight
}

// phaseSnap pairs a finished phase with the metadata its report section needs.
type phaseSnap struct {
	kind string
	snap Snapshot
	meta map[string]chanMeta
}

// runPlain drives the engine non-interactively from the resolved config, one
// phase at a time (messages then reactions, or the configured order). Used when
// there is no terminal (pipes, cron, CI) or when --no-tui is set.
func runPlain(in plainRun) {
	cfg := in.cfg
	phases := plainPhases(in)
	if len(phases) == 0 {
		if !cfg.delMessages && !cfg.delReactions {
			fmt.Println("Nothing selected to delete.")
		} else {
			fmt.Println("Nothing matched your filters (or everything was already done). Nothing to do.")
		}
		return
	}
	if cfg.execute && cfg.token == "" {
		fmt.Fprintln(os.Stderr, "error: execute needs a token (set DISCORD_TOKEN or pass --token).")
		os.Exit(2)
	}
	// One identity check up front: you can only delete your own account's data, so
	// a mismatch just wastes requests on 403s. Applies to every phase.
	if cfg.execute && in.owner.ID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		id := fetchTokenIdentity(ctx, cfg.token)
		cancel()
		if id.state == tsValid && id.id != "" && id.id != in.owner.ID {
			exportedBy := accountLabel(in.owner.Handle, in.owner.Name, in.owner.ID)
			signedInAs := accountLabel(id.handle, id.name, id.id)
			fmt.Fprintf(os.Stderr, "error: wrong account. This package was exported by %s, but your token is %s. A package can only be cleared by the account that made it. Sign in as %s, or request a fresh export from %s.\n",
				exportedBy, signedInAs, exportedBy, signedInAs)
			os.Exit(2)
		}
	}

	// An execute run must be resumable: without a working log every deletion
	// would be re-attempted by the next run, so refuse rather than run blind.
	if cfg.execute {
		for _, p := range phases {
			if err := probeProgressLog(p.progPath); err != nil {
				fmt.Fprintf(os.Stderr, "error: cannot open the resume log (%v). Deletions would not be recorded, and the next run would repeat all of them. Point DISCORD_DELETE_STATE_DIR at a writable directory.\n", err)
				os.Exit(2)
			}
		}
	}

	for _, p := range phases {
		est := estimate(p.jobs, cfg.workers, p.floor, cfg.maxRPS)
		fmt.Printf("%s: %s %s across %d channel(s), est. runtime ~%s.\n",
			p.kind, commafy(p.total), nounFor(p.kind, p.total), len(p.jobs), fmtDur(est))
		if p.summary != "" {
			fmt.Println("  " + p.summary)
		}
	}
	if cfg.execute {
		fmt.Print("\n\033[1mEXECUTE mode: this permanently removes the items above.\033[0m Starting in 4s (Ctrl-C to abort).\n")
		time.Sleep(4 * time.Second)
	} else {
		fmt.Println("\nDRY RUN (nothing is deleted). Pass --execute to delete for real.")
	}

	target := resolveNtfyURL(cfg.ntfy)
	notify := plainNotify{
		target:  target,
		control: controlTarget(target),
		every:   cfg.notifyEvery,
		pkg:     in.pkgName,
		execute: cfg.execute,
	}

	// Remote control subscribes once for the whole run, not per phase: the ntfy
	// stream only delivers messages posted while it is connected, so a command
	// sent between phases would land with nobody listening.
	ctrlCtx, stopCtrl := context.WithCancel(context.Background())
	defer stopCtrl()
	var ctrl chan controlCmd
	if cfg.execute && notify.control != "" {
		ctrl = make(chan controlCmd, 8)
		go subscribeControl(ctrlCtx, notify.control, func(c controlCmd) {
			select {
			case ctrl <- c:
			case <-ctrlCtx.Done():
			}
		})
	}

	// The invalid-response window is per IP, so both phases share one budget.
	cfb := newCFBudget()

	startedAt := time.Now()
	var snaps []phaseSnap
	aborted := false
	stopped := false
	startPaused := false
	for i, p := range phases {
		if len(phases) > 1 {
			fmt.Printf("\n--- phase %d/%d: %s ---\n", i+1, len(phases), p.kind)
		}
		snap := drivePlainPhase(cfg, p, notify, ctrl, startPaused, cfb)
		snaps = append(snaps, phaseSnap{kind: p.kind, snap: snap, meta: p.meta})
		printPhaseResult(cfg, p.kind, snap)
		if snap.Aborted {
			aborted = true
			// A 401 abort won't fix itself by moving on, so stop before the next phase.
			break
		}
		if !snap.Completed {
			// A stop (Ctrl-C/SIGTERM/remote) ends the whole run, not just the phase.
			break
		}
		// Commands that arrived while this phase wound down had no engine to act
		// on; apply them before the next phase starts.
		stopped, startPaused = drainControl(ctrl)
		if stopped {
			fmt.Println("stopping (remote)...")
			break
		}
	}

	if aborted {
		fmt.Println("Run was aborted early (see status). Re-run to resume; already-done items are not repeated.")
		// Forget the stored token only when it was the token in use: a 401 on
		// a bad --token says nothing about the keyring token.
		if in.tokenFromStore && in.stateKey != "" && hasStoredToken(in.stateKey) {
			if err := forgetToken(in.stateKey); err == nil {
				fmt.Println("The stored token was rejected (401) and has been forgotten.")
			} else {
				fmt.Fprintf(os.Stderr, "The stored token was rejected (401) but could not be removed from the keyring: %v\n", err)
			}
		}
	} else if stopped || anyStopped(snaps) {
		fmt.Println("Stopped before finishing. Re-run to resume where you left off.")
	}
	if !cfg.execute {
		fmt.Println("This was a DRY RUN. Re-run with --execute to actually delete.")
	}

	writePlainReport(in, cfg, startedAt, snaps, messageResumed(phases))
}

// plainPhases builds the ordered, non-empty phases for a headless run, printing a
// resume/empty note per phase. Bounds/content errors exit, matching prior behavior.
func plainPhases(in plainRun) []plainPhase {
	cfg := in.cfg
	bounds, err := resolveBounds(cfg.afterSnow, cfg.beforeSnow, cfg.afterDate, cfg.beforeDate, cfg.last, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	raws := in.pkg.Raws

	var msg, react *plainPhase
	if cfg.delMessages {
		substr, re, cerr := compileContentFilter(cfg.content)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", cerr)
			os.Exit(2)
		}
		done := loadProgressSet(in.progPath)
		f := Filter{
			Content: substr, ContentRe: re,
			AfterID: bounds.AfterID, BeforeID: bounds.BeforeID,
			Order: cfg.order, Channels: selectionSet(raws, in.sel),
			Types: typesMask(cfg.typeSel), Done: done,
		}
		jobs, total := ApplyFilter(raws, f)
		resumed := countInSet(raws, done)
		if resumed > 0 {
			fmt.Printf("Resuming: %s already-deleted message(s) skipped.\n", commafy(resumed))
		}
		if total == 0 {
			fmt.Println("No messages matched your filters (or all were already deleted).")
		} else {
			msg = &plainPhase{
				kind: "messages", jobs: jobs, total: total,
				floor:    time.Duration(cfg.delay * float64(time.Second)),
				progPath: in.progPath, meta: metaFromRaws(raws),
				resumed: resumed, summary: filterSummary(f, cfg.order),
			}
		}
	}
	if cfg.delReactions {
		done := loadProgressSet(in.reactProgPath)
		f := Filter{
			AfterID: bounds.AfterID, BeforeID: bounds.BeforeID,
			Order: cfg.order, Channels: in.reactSel, Done: done,
		}
		jobs, total := ApplyReactionFilter(in.pkg.Reactions, f, in.pkg.GuildNames)
		if total == 0 {
			fmt.Println("No reactions matched your filters (or all were already removed).")
		} else {
			react = &plainPhase{
				kind: "reactions", jobs: jobs, total: total,
				floor:    time.Duration(cfg.reactionDelay * float64(time.Second)),
				progPath: in.reactProgPath,
				meta:     metaFromReactions(in.pkg.Reactions, in.pkg.GuildNames),
				summary:  "reactions share the date and order filters",
			}
		}
	}

	first, second := msg, react
	if cfg.reactionsFirst {
		first, second = react, msg
	}
	var phases []plainPhase
	if first != nil {
		phases = append(phases, *first)
	}
	if second != nil {
		phases = append(phases, *second)
	}
	return phases
}

// drivePlainPhase runs one phase's engine to completion (or interrupt) and
// returns its final snapshot. ctrl carries the run's remote commands; startPaused
// holds a pause that arrived at the previous phase boundary; cfb is the run's
// shared Cloudflare invalid-response window.
func drivePlainPhase(cfg runConfig, p plainPhase, notify plainNotify, ctrl <-chan controlCmd, startPaused bool, cfb *cfBudget) Snapshot {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stats := NewStats(p.total, cfg.workers)

	// Record confirmed-gone items so a later run resumes instead of re-requesting.
	var pl *progressLog
	var onDeleted func(string)
	if cfg.execute {
		if opened, err := openProgressLog(p.progPath); err == nil {
			pl = opened
			onDeleted = pl.record
		} else {
			// The preflight probe in runPlain passed, so the state dir broke
			// mid-run; warn and carry on.
			fmt.Fprintf(os.Stderr, "warning: resume log unavailable (%v); this phase's deletions will be re-attempted on the next run.\n", err)
		}
	}
	eng := NewEngine(EngineConfig{
		Token:             cfg.token,
		Workers:           cfg.workers,
		DeleteDelay:       p.floor,
		Jitter:            cfg.jitter,
		DryRun:            !cfg.execute,
		GlobalMinInterval: cfg.minInterval(),
		OnDeleted:         onDeleted,
		CF:                cfb,
	}, stats)
	if startPaused {
		eng.setPaused(true)
		fmt.Println("paused (remote)")
	}
	notify.kind = p.kind
	done := eng.RunAsync(ctx, p.jobs)
	plainReport(ctx, cancel, stats, eng, notify, ctrl)
	// Workers can still call record until done closes: cancel, drain, then close.
	cancel()
	closeAfterEngine(pl, done)
	if werr := pl.writeErr(); werr != nil {
		fmt.Fprintf(os.Stderr, "warning: resume log writes failed (%v); deletions after the failure will be re-attempted on the next run.\n", werr)
	}
	return stats.Snapshot()
}

// drainControl consumes the commands that arrived between phases, where no
// engine was live to apply them, and reports whether the run was stopped and
// whether the next phase starts paused.
func drainControl(ctrl <-chan controlCmd) (stop, paused bool) {
	for {
		select {
		case c := <-ctrl:
			switch c {
			case cmdStop:
				return true, paused
			case cmdPause:
				paused = true
			case cmdResume:
				paused = false
			}
		default:
			return false, paused
		}
	}
}

// writePlainReport assembles the combined multi-phase report, fetching guild
// membership once for the undeletable rollup, then writes it and pings ntfy.
func writePlainReport(in plainRun, cfg runConfig, startedAt time.Time, snaps []phaseSnap, resumed int) {
	needMembers := false
	for _, s := range snaps {
		if len(s.snap.Forbidden) > 0 {
			needMembers = true
		}
	}
	var members map[string]bool
	if cfg.execute && strings.TrimSpace(cfg.token) != "" && needMembers {
		mctx, mcancel := context.WithTimeout(context.Background(), 5*time.Second)
		members = fetchGuildMembership(mctx, cfg.token)
		mcancel()
	}
	var results []opResult
	for _, s := range snaps {
		results = append(results, opResult{
			Kind:      s.kind,
			Snap:      s.snap,
			Collapsed: s.snap.ActiveLimit >= 1 && s.snap.ActiveLimit < len(s.snap.Workers),
			Forbidden: forbiddenByServer(s.snap.Forbidden, s.meta, members),
		})
	}
	r := runReport{
		Package:   in.pkgName,
		Execute:   cfg.execute,
		StartedAt: startedAt,
		EndedAt:   time.Now(),
		Results:   results,
		Resumed:   resumed,
	}
	if path := r.destPath(in.reportOverride, in.progPath); path != "" {
		if err := writeRunReport(path, r); err == nil {
			fmt.Printf("Report written to %s\n", plainPathLink(path))
		}
	}
	if target := resolveNtfyURL(cfg.ntfy); target != "" {
		if err := sendNtfy(context.Background(), target, r.notifyTitle(), r.notifyBody(), r.notifyPriority(), r.notifyTags()); err != nil {
			fmt.Fprintf(os.Stderr, "ntfy notification failed: %v\n", err)
		}
	}
}

// printPhaseResult prints one phase's outcome line.
func printPhaseResult(cfg runConfig, kind string, snap Snapshot) {
	o := opResult{Kind: kind}
	verb := o.verb(cfg.execute)
	if verb != "" {
		verb = strings.ToUpper(verb[:1]) + verb[1:]
	}
	fmt.Printf("\n%s %s %s · skipped %s · failed %s · in %s\n",
		verb, commafy(int(snap.Deleted)), nounFor(kind, int(snap.Deleted)),
		commafy(int(snap.Skipped)), commafy(int(snap.Failed)), fmtDur(snap.Elapsed))
}

// nounFor is the singular/plural item noun for a phase kind.
func nounFor(kind string, n int) string {
	noun := "message"
	if kind == "reactions" {
		noun = "reaction"
	}
	if n == 1 {
		return noun
	}
	return noun + "(s)"
}

// anyStopped reports whether a phase ended without processing all its work and
// without a hard abort (a user stop / interrupt).
func anyStopped(snaps []phaseSnap) bool {
	for _, s := range snaps {
		if !s.snap.Completed && !s.snap.Aborted {
			return true
		}
	}
	return false
}

// messageResumed pulls the message phase's resumed count for the report (a
// message-only concept), or 0 when no message phase ran.
func messageResumed(phases []plainPhase) int {
	for _, p := range phases {
		if p.kind == "messages" {
			return p.resumed
		}
	}
	return 0
}

func selectionSet(raws []RawChannel, sel map[string]bool) map[string]bool {
	all := true
	set := map[string]bool{}
	for _, rc := range raws {
		if sel[rc.ChannelID] {
			set[rc.ChannelID] = true
		} else {
			all = false
		}
	}
	if all {
		return nil
	}
	return set
}

// plainNotify carries the ntfy settings the headless reporter needs for periodic
// progress pushes and remote control.
type plainNotify struct {
	target  string // resolved status URL ("" = notifications off)
	control string // derived control URL for pause/resume/stop
	pkg     string
	kind    string        // in-flight phase kind, for the rate unit
	every   time.Duration // 0 = periodic off (completion ping still fires in main)
	execute bool
}

// plainReport prints periodic progress lines until the run ends or is signalled.
// On a real run with ntfy set it also pushes a progress notification every
// n.every (each with a pause button) and applies pause/resume/stop commands
// arriving on ctrl, the run-wide control subscription.
func plainReport(ctx context.Context, cancel context.CancelFunc, stats *Stats, eng *Engine, n plainNotify, ctrl <-chan controlCmd) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastNotify := time.Now()
	lastLine := ""
	report := func() {
		s := stats.Snapshot()
		pct := 0.0
		if s.Total > 0 {
			pct = float64(s.Processed) / float64(s.Total) * 100
		}
		line := fmt.Sprintf("[%5.1f%%] deleted %d  skipped %d  failed %d  (%d/%d)  %.2f/s  eta %s",
			pct, s.Deleted, s.Skipped, s.Failed, s.Processed, s.Total, s.Rate, etaStr(s, eng.isPaused()))
		if line != lastLine {
			fmt.Println(line)
			lastLine = line
		}
		if s.Status != "" {
			fmt.Println("  · " + s.Status)
		}
	}
	pushProgress := func(paused bool) {
		if n.target == "" {
			return
		}
		_ = postNtfy(context.Background(), n.target, runningNtfy(n.pkg, n.kind, stats.Snapshot(), paused, n.control))
	}
	periodic := func() {
		if !n.execute || n.every <= 0 || n.target == "" {
			return
		}
		if time.Since(lastNotify) < n.every {
			return
		}
		lastNotify = time.Now()
		pushProgress(eng.isPaused())
	}
	for {
		select {
		case <-sig:
			fmt.Println("stopping (safe/resumable)...")
			cancel()
			return
		case c := <-ctrl:
			switch c {
			case cmdPause:
				if !eng.isPaused() {
					eng.setPaused(true)
					lastNotify = time.Now()
					fmt.Println("paused (remote)")
					pushProgress(true)
				}
			case cmdResume:
				if eng.isPaused() {
					eng.setPaused(false)
					lastNotify = time.Now()
					fmt.Println("resumed (remote)")
					pushProgress(false)
				}
			case cmdStop:
				fmt.Println("stopping (remote)...")
				cancel()
				return
			}
		case <-ticker.C:
			report()
			periodic()
			s := stats.Snapshot()
			if s.Finished || s.Aborted {
				report()
				return
			}
		}
	}
}

// filterSummary describes the active filters and order for the preflight.
func filterSummary(f Filter, order string) string {
	var parts []string
	parts = append(parts, "order: "+order+" first")
	const dateFmt = "2006-01-02 15:04"
	switch {
	case f.AfterID != 0 && f.BeforeID != 0:
		parts = append(parts, fmt.Sprintf("dates: %s → %s",
			snowflakeToTime(f.AfterID).Format(dateFmt), snowflakeToTime(f.BeforeID).Format(dateFmt)))
	case f.AfterID != 0:
		parts = append(parts, "dates: after "+snowflakeToTime(f.AfterID).Format(dateFmt))
	case f.BeforeID != 0:
		parts = append(parts, "dates: before "+snowflakeToTime(f.BeforeID).Format(dateFmt))
	}
	if f.Content != "" {
		parts = append(parts, fmt.Sprintf("content contains %q", f.Content))
	}
	if f.Types != 0 {
		parts = append(parts, "type: "+describeTypes(f.Types))
	}
	if len(f.Guilds) > 0 {
		parts = append(parts, fmt.Sprintf("%d guild filter(s)", len(f.Guilds)))
	}
	if len(f.Channels) > 0 {
		parts = append(parts, fmt.Sprintf("%d channel filter(s)", len(f.Channels)))
	}
	return "Filters: " + strings.Join(parts, "  ·  ")
}

func estimate(jobs []ChannelJob, workers int, perDelete time.Duration, maxRPS float64) time.Duration {
	if workers < 1 {
		workers = 1
	}
	// Longest-processing-time bound: greedily pack channels onto `workers`
	// buckets, runtime ≈ the fullest bucket. Good enough for a preflight ETA.
	buckets := make([]int64, workers)
	var total int64
	for _, j := range jobs {
		min, idx := buckets[0], 0
		for i, v := range buckets {
			if v < min {
				min, idx = v, i
			}
		}
		buckets[idx] += int64(j.count())
		total += int64(j.count())
	}
	var maxCount int64
	for _, v := range buckets {
		if v > maxCount {
			maxCount = v
		}
	}
	est := time.Duration(maxCount) * perDelete
	// The account-wide limiter spaces every request at 1/maxRPS, so no worker
	// count gets the run under total/maxRPS.
	if maxRPS > 0 {
		if capped := time.Duration(float64(total) / maxRPS * float64(time.Second)); capped > est {
			est = capped
		}
	}
	return est
}

// parseTypes turns a comma-separated --type value into a selection set,
// rejecting unknown ids. Empty -> nil (meaning "any type").
func parseTypes(csv string) (map[string]bool, error) {
	sel := map[string]bool{}
	for _, p := range strings.Split(csv, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if typeIDMask(p) == 0 {
			return nil, fmt.Errorf("unknown --type %q (valid: text,media,image,video,audio,voice,file,link)", p)
		}
		sel[p] = true
	}
	if len(sel) == 0 {
		return nil, nil
	}
	return sel, nil
}

func toSet(csv string) map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Split(csv, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			m[p] = true
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// parseSnowflake parses a message ID bound. A malformed value errors rather
// than dropping the bound, which would widen the deletion range.
func parseSnowflake(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid message ID %q (must be a whole number)", s)
	}
	return n, nil
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func usage() {
	fmt.Fprint(os.Stderr, `discord-delete: bulk-delete your Discord messages and reactions from your data package.

USAGE:
  discord-delete [--package PATH] [flags]

Run in a terminal without options and a guide menu opens.

FLAGS:
`)
	flag.PrintDefaults()
	fmt.Fprint(os.Stderr, `
EXAMPLES:
  discord-delete --package package.zip
  discord-delete --package ./package --guild 111,222
  discord-delete --package package.zip --content "oops"
  discord-delete --package package.zip --type image,video
  discord-delete --package package.zip --type voice --last 30d --order newest
  discord-delete --package package.zip --after-date 2023-01-01 --before-date 2023-12-31

Automating a user account breaks Discord's ToS and carries some ban risk.
`)
}
