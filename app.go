package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// screen is the current top-level view of the app.
type screen int

const (
	scHome screen = iota
	scConfigure
	scChannels
	scTypes
	scConfirm
	scRunning
)

// chanScope selects which channel set the shared tree selector (scChannels)
// drives: the message channels or the reaction channels.
type chanScope int

const (
	scopeMessages chanScope = iota
	scopeReactions
)

// runConfig holds every tunable, mirroring the CLI flags. The TUI edits these
// live; nothing here is required except (for a real run) a token.
type runConfig struct {
	order       string // "oldest" | "newest"
	content     string
	afterSnow   string // from --after (CLI only)
	beforeSnow  string // from --before (CLI only)
	afterDate   string
	beforeDate  string
	last        string
	typeSel     map[string]bool // selected message-type ids (nil/empty = any)
	workers     int
	delay       float64
	jitter      float64
	maxRPS      float64
	token       string
	execute     bool
	ntfy        string        // ntfy topic or full URL for a completion ping ("" = off)
	notifyEvery time.Duration // push a progress ntfy this often during a run (0 = completion only)
	remember    bool          // persist the token locally (keychain / encrypted file)

	// dual-mode: what to delete, from what the package contains
	delMessages    bool    // run the message phase
	delReactions   bool    // run the reaction phase
	reactionsFirst bool    // reaction phase runs before messages
	reactionDelay  float64 // per-channel spacing floor for reactions (seconds)
}

// appModel is the whole TUI: a small state machine over the screens above,
// sharing one loaded package and one live-filtered preview.
type appModel struct {
	screen  screen
	raws    []RawChannel
	pkgName string

	cfg      runConfig
	selected map[string]bool // channelID -> included

	// live preview (recomputed whenever the filter changes)
	jobs  []ChannelJob
	total int
	perr  string // filter/parse error to surface

	// configure screen
	fcursor  int
	editing  bool
	input    textinput.Model
	advanced bool

	// token validation (async /users/@me probe)
	tokenState  tokenState
	tokenUser   string
	tokenErr    string
	tokenID     string // the token account's user id (for owner-match check)
	tokenHandle string // unique @handle, for the mismatch note

	// package owner (from account/user.json); "" if the package didn't record it
	ownerID     string
	ownerName   string
	ownerHandle string // unique @handle, for the mismatch note

	// update notice (async; see update.go)
	updateCh   <-chan string
	updateLine string // "" until a newer version is found

	// guild membership for the 403 rollup, fetched once per run
	members       map[string]bool
	membersLoaded bool

	// opt-in token storage (keychain / encrypted file), keyed per account
	stateKey       string // account key for the store (same as the resume-log key)
	savedToken     string // the token value currently persisted, to avoid re-saving
	tokenFromStore bool
	storeNote      string // short status from the last save/forget, shown in Configure

	// ntfy test ping: result of the notification sent when the topic is entered
	ntfyNote string

	// crash-safe resume: already-deleted message IDs from prior runs, and the
	// per-package log the engine appends to during a real run.
	done     map[string]bool
	progPath string
	progLog  *progressLog
	resumed  int // count of package messages already deleted in a prior run

	// reactions (from Activity/reporting), parallel to the message fields
	caps          PackageCapabilities
	reactions     []Reaction
	guildNames    map[string]string
	reactSelected map[string]bool // reaction channel ids -> included
	reactGuilds   []guildGroup    // guild-grouped reaction channels for the selector
	reactRaws     []RawChannel    // synthetic channels backing the reaction selector
	reactJobs     []ChannelJob
	reactTotal    int
	reactDone     map[string]bool // reaction keys removed in a prior run
	reactProgPath string

	// two-phase run state (messages phase, reactions phase)
	runCtx       context.Context
	phases       []phasePlan
	phaseIdx     int
	phaseResults []opResult

	// browser sign-in (async Chrome-launch capture)
	browserActive bool
	browserErr    string
	browserCancel context.CancelFunc

	// channels screen (shared by the message and reaction selectors)
	chanScope chanScope
	guilds    []guildGroup
	ccursor   int
	search    textinput.Model

	// message-type screen
	tcursor    int
	typeCounts map[string]int // static per-type message tallies

	// running screen
	stats    *Stats
	eng      *Engine // live handle for pause/resume + pacing hotkeys
	paused   bool
	cancel   context.CancelFunc
	prog     progress.Model
	started  bool
	rateHist []float64 // recent deletions/sec samples for the sparkline
	logWarn  string    // resume-log failure notice; "" while the log is healthy
	cfb      *cfBudget // Cloudflare invalid-response window; one per process, not per phase

	// ntfy progress + remote control (execute runs with ntfy set)
	lastNotify time.Time       // when the last progress ntfy went out
	controlCh  chan controlCmd // pause/resume/stop from the phone; nil = control off
	controlOn  bool
	stopping   bool // stop taken; finalize instead of advancing to the next phase
	pausePend  bool // pause taken at a phase boundary; applied to the next phase's engine

	// end-of-run finalization (report file + optional ntfy ping), fired once
	startedAt      time.Time
	reported       bool
	reportPath     string // where the run report was written ("" = not yet / failed)
	reportOverride string // --report path; "" = default alongside the resume log
	notifyResult   string // short outcome of the ntfy ping, shown on the final frame
	reportHit      hitBox // where the final frame's open button sits, for click handling
	openErr        string // last failure from opening the report

	width, height int
	quitting      bool
}

// guildGroup buckets channels under a server (or the synthetic DM group).
type guildGroup struct {
	id     string
	name   string
	isDM   bool
	open   bool
	chans  []int // indices into m.raws
	msgSum int
}

func newAppModel(raws []RawChannel, cfg runConfig, sel map[string]bool, pkgName string) *appModel {
	ti := textinput.New()
	ti.Prompt = "› "
	sr := textinput.New()
	sr.Prompt = "/ "
	sr.Placeholder = "filter channels…"
	staticCursorForDemo(&ti)
	staticCursorForDemo(&sr)

	p := progress.New(progress.WithGradient(nord7, nord8), progress.WithWidth(50), progress.WithoutPercentage())

	m := &appModel{
		screen:   scHome,
		raws:     raws,
		pkgName:  pkgName,
		cfg:      cfg,
		selected: sel,
		input:    ti,
		search:   sr,
		prog:     p,
		width:    90,
		height:   30,
		cfb:      newCFBudget(),
	}
	// Messages are known at construction; reactions arrive later via setReactions.
	m.caps.HasMessages = len(raws) > 0
	m.typeCounts = countTypes(raws)
	m.buildGuilds()
	m.recompute()
	return m
}

// Init validates a token supplied up front (via --token or DISCORD_TOKEN) so
// the home screen shows "logged in as X", and collects the update notice.
func (m *appModel) Init() tea.Cmd {
	return tea.Batch(m.startTokenCheck(), awaitUpdateNotice(m.updateCh))
}

// updateNoticeMsg carries the update-check result to the home screen.
type updateNoticeMsg string

// awaitUpdateNotice delivers the update-check result, or nothing when no check
// was started (tests build the model directly).
func awaitUpdateNotice(ch <-chan string) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg { return updateNoticeMsg(<-ch) }
}

// --- preview ---------------------------------------------------------------

func (m *appModel) selectedSet() map[string]bool {
	return selectionSet(m.raws, m.selected)
}

// reactSelectedSet is selectedSet for the reaction channel selector.
func (m *appModel) reactSelectedSet() map[string]bool {
	return selectionSet(m.reactRaws, m.reactSelected)
}

// scopeRaws / scopeSelected / scopeGuilds return the channel set the tree
// selector currently operates on. The returned slices/maps are the model's own
// (not copies), so mutating an element (a guild's open flag, a selection bit)
// edits the live state.
func (m *appModel) scopeRaws() []RawChannel {
	if m.chanScope == scopeReactions {
		return m.reactRaws
	}
	return m.raws
}

func (m *appModel) scopeSelected() map[string]bool {
	if m.chanScope == scopeReactions {
		return m.reactSelected
	}
	return m.selected
}

func (m *appModel) scopeGuilds() []guildGroup {
	if m.chanScope == scopeReactions {
		return m.reactGuilds
	}
	return m.guilds
}

// scopeNoun labels the per-channel count in the current scope's tree.
func (m *appModel) scopeNoun() string {
	if m.chanScope == scopeReactions {
		return "reactions"
	}
	return "msgs"
}

// countSelected tallies how many of raws are selected.
func countSelected(raws []RawChannel, sel map[string]bool) (selected, total int) {
	for _, rc := range raws {
		total++
		if sel[rc.ChannelID] {
			selected++
		}
	}
	return
}

// reactSelectionCounts is selectionCounts for the reaction channel selector.
func (m *appModel) reactSelectionCounts() (selected, total int) {
	return countSelected(m.reactRaws, m.reactSelected)
}

// scopeSelectionCounts is selectionCounts for whichever tree is on screen.
func (m *appModel) scopeSelectionCounts() (selected, total int) {
	return countSelected(m.scopeRaws(), m.scopeSelected())
}

// recompute refreshes both the message and reaction previews from the filters.
func (m *appModel) recompute() {
	m.recomputeMessages()
	m.recomputeReactions()
}

func (m *appModel) recomputeMessages() {
	bounds, err := resolveBounds(m.cfg.afterSnow, m.cfg.beforeSnow, m.cfg.afterDate, m.cfg.beforeDate, m.cfg.last, time.Now())
	if err != nil {
		m.perr = err.Error()
		m.jobs, m.total = nil, 0
		return
	}
	substr, re, err := compileContentFilter(m.cfg.content)
	if err != nil {
		m.perr = err.Error()
		m.jobs, m.total = nil, 0
		return
	}
	m.perr = ""
	f := Filter{
		Content:   substr,
		ContentRe: re,
		AfterID:   bounds.AfterID,
		BeforeID:  bounds.BeforeID,
		Order:     m.cfg.order,
		Channels:  m.selectedSet(),
		Types:     typesMask(m.cfg.typeSel),
		Done:      m.done,
	}
	m.jobs, m.total = ApplyFilter(m.raws, f)
}

// recomputeReactions refreshes the reaction preview. Reactions share the date and
// order filters with messages, plus their own channel selection and resume set.
func (m *appModel) recomputeReactions() {
	if !m.caps.HasReactions {
		m.reactJobs, m.reactTotal = nil, 0
		return
	}
	bounds, err := resolveBounds(m.cfg.afterSnow, m.cfg.beforeSnow, m.cfg.afterDate, m.cfg.beforeDate, m.cfg.last, time.Now())
	if err != nil {
		m.reactJobs, m.reactTotal = nil, 0
		return
	}
	f := Filter{
		AfterID:  bounds.AfterID,
		BeforeID: bounds.BeforeID,
		Order:    m.cfg.order,
		Channels: m.reactSelectedSet(),
		Done:     m.reactDone,
	}
	m.reactJobs, m.reactTotal = ApplyReactionFilter(m.reactions, f, m.guildNames)
}

// setReactions installs the reaction data on the model and builds the reaction
// channel selector, then defaults the delete targets from what the package has.
func (m *appModel) setReactions(reactions []Reaction, guildNames map[string]string, caps PackageCapabilities, reactProgPath string) {
	m.caps = caps
	m.reactions = reactions
	m.guildNames = guildNames
	m.reactProgPath = reactProgPath
	m.reactDone = loadProgressSet(reactProgPath)
	m.reactRaws = reactionRawChannels(reactions, guildNames)
	m.reactGuilds = groupGuilds(m.reactRaws)
	m.reactSelected = map[string]bool{}
	for _, rc := range m.reactRaws {
		m.reactSelected[rc.ChannelID] = true
	}
	m.cfg.delMessages = caps.HasMessages
	m.cfg.delReactions = caps.HasReactions && !caps.HasMessages
	m.recompute()
}

// maybeSaveToken persists the current token to the OS keyring when "remember" is
// on and the token validated, deduping against what's already stored.
func (m *appModel) maybeSaveToken() {
	tok := strings.TrimSpace(m.cfg.token)
	if !m.cfg.remember || m.stateKey == "" || tok == "" || tok == m.savedToken {
		return
	}
	if m.tokenState != tsValid {
		return // only ever store a token we've confirmed works
	}
	backend, err := saveToken(m.stateKey, tok)
	if err != nil {
		m.storeNote = "not remembered: " + err.Error()
		return
	}
	m.savedToken = tok
	m.storeNote = "token remembered (" + backend + ")"
}

// forgetStoredToken removes any persisted token for this account.
func (m *appModel) forgetStoredToken() {
	if m.stateKey == "" {
		return
	}
	if err := forgetToken(m.stateKey); err != nil {
		m.storeNote = "forget failed: " + err.Error()
		return
	}
	m.savedToken, m.tokenFromStore = "", false
	m.storeNote = "stored token forgotten"
}

// resetDefaults restores every config knob to its default and re-selects all
// channels, keeping the token (a credential, not config) and its check state.
func (m *appModel) resetDefaults() {
	tok := m.cfg.token
	m.cfg = defaultRunConfig()
	m.cfg.token = tok
	// Re-select everything in both trees, independent of which selector was last
	// open, and restore the capability-derived delete targets.
	for _, rc := range m.raws {
		m.selected[rc.ChannelID] = true
	}
	for _, rc := range m.reactRaws {
		m.reactSelected[rc.ChannelID] = true
	}
	m.cfg.delMessages = m.caps.HasMessages
	m.cfg.delReactions = m.caps.HasReactions && !m.caps.HasMessages
	m.fcursor = 0
	m.perr = ""
	m.recompute()
}

func (m *appModel) previewETA() time.Duration {
	return estimate(m.jobs, m.cfg.workers, time.Duration(m.cfg.delay*float64(time.Second)))
}

// --- top-level Update ------------------------------------------------------

func (m *appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.prog.Width = clampInt(msg.Width-20, 20, 70)
		return m, nil
	case tickMsg:
		if m.screen == scRunning {
			snap := m.stats.Snapshot()
			m.rateHist = append(m.rateHist, snap.Rate)
			if len(m.rateHist) > 120 {
				m.rateHist = m.rateHist[len(m.rateHist)-120:]
			}
			m.noteLogErr()
			if snap.Finished || snap.Aborted {
				if m.reported {
					return m, nil
				}
				// Record this phase's result. Then either advance to the next
				// phase, or (on the last phase, an abort, or a stop) finalize
				// once. A stop ends the whole run, not just the phase: the next
				// phase would start on the already-cancelled context.
				m.phaseResults = append(m.phaseResults, m.phaseResult(snap))
				if m.progLog != nil {
					m.progLog.close()
					m.noteLogErr()
					m.progLog = nil
				}
				if snap.Aborted || !snap.Completed || m.stopping || m.phaseIdx >= len(m.phases)-1 {
					m.reported = true
					return m, m.finalizeRun()
				}
				m.phaseIdx++
				return m, m.startPhase(m.phaseIdx)
			}
			cmds := []tea.Cmd{doTick()}
			if c := m.maybePeriodicNotify(); c != nil {
				cmds = append(cmds, c)
			}
			return m, tea.Batch(cmds...)
		}
		return m, nil
	case controlMsg:
		return m.applyControl(msg.cmd)
	case notifyDoneMsg:
		if msg.err != nil {
			m.notifyResult = "notify failed: " + msg.err.Error()
		} else if strings.TrimSpace(m.cfg.ntfy) != "" {
			m.notifyResult = "notified via ntfy"
		}
		return m, nil
	case ntfyTestMsg:
		if msg.err != nil {
			m.ntfyNote = "test notification failed: " + msg.err.Error()
		} else {
			m.ntfyNote = "test notification sent; check your ntfy app"
		}
		return m, nil
	case tokenCheckMsg:
		m.applyTokenCheck(msg)
		return m, nil
	case updateNoticeMsg:
		m.updateLine = string(msg)
		return m, nil
	case browserSigninMsg:
		return m, m.applyBrowserSignin(msg)
	}

	switch m.screen {
	case scHome:
		return m.updateHome(msg)
	case scConfigure:
		return m.updateConfigure(msg)
	case scChannels:
		return m.updateChannels(msg)
	case scTypes:
		return m.updateTypes(msg)
	case scConfirm:
		return m.updateConfirm(msg)
	case scRunning:
		return m.updateRunning(msg)
	}
	return m, nil
}

func (m *appModel) View() string {
	switch m.screen {
	case scConfigure:
		return m.viewConfigure()
	case scChannels:
		return m.viewChannels()
	case scTypes:
		return m.viewTypes()
	case scConfirm:
		return m.viewConfirm()
	case scRunning:
		return m.viewRunning()
	default:
		return m.viewHome()
	}
}

// --- home ------------------------------------------------------------------

func (m *appModel) updateHome(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "c":
		m.screen = scConfigure
		return m, nil
	case "e":
		m.toggleExecute()
		return m, nil
	case "enter", "s":
		return m.startRun()
	}
	return m, nil
}

// toggleExecute flips dry-run/execute, refusing execute without a token (or with
// one Discord has already told us is invalid).
func (m *appModel) toggleExecute() {
	if !m.cfg.execute {
		if reason := m.executeGuard(); reason != "" {
			m.perr = reason
			return
		}
	}
	m.cfg.execute = !m.cfg.execute
	m.perr = ""
}

func (m *appModel) startRun() (tea.Model, tea.Cmd) {
	// Startable when ANY enabled phase has work; gating on the message count
	// alone would make a reactions-only run unreachable.
	msgWork := m.cfg.delMessages && m.total > 0
	reactWork := m.cfg.delReactions && m.reactTotal > 0
	if !msgWork && !reactWork {
		m.perr = "Nothing matches the current filters."
		return m, nil
	}
	if m.cfg.execute {
		if reason := m.executeGuard(); reason != "" {
			m.perr = reason
			return m, nil
		}
		m.screen = scConfirm
		return m, nil
	}
	return m.launchEngine()
}

// phasePlan is one phase of a run (messages or reactions), with its jobs, the
// per-channel floor, and the resume log it appends to.
type phasePlan struct {
	kind    string
	jobs    []ChannelJob
	total   int
	floor   time.Duration
	logPath string
}

// buildPhases assembles the run's phases from the selected targets, in the
// configured order (messages first by default).
func (m *appModel) buildPhases() []phasePlan {
	var msg, react *phasePlan
	if m.cfg.delMessages && m.total > 0 {
		msg = &phasePlan{
			kind: "messages", jobs: m.jobs, total: m.total,
			floor: time.Duration(m.cfg.delay * float64(time.Second)), logPath: m.progPath,
		}
	}
	if m.cfg.delReactions && m.reactTotal > 0 {
		react = &phasePlan{
			kind: "reactions", jobs: m.reactJobs, total: m.reactTotal,
			floor: time.Duration(m.cfg.reactionDelay * float64(time.Second)), logPath: m.reactProgPath,
		}
	}
	first, second := msg, react
	if m.cfg.reactionsFirst {
		first, second = react, msg
	}
	var out []phasePlan
	if first != nil {
		out = append(out, *first)
	}
	if second != nil {
		out = append(out, *second)
	}
	return out
}

// launchEngine starts the run: builds the phases and starts the first one, plus
// the ntfy remote-control subscriber for the whole run.
func (m *appModel) launchEngine() (tea.Model, tea.Cmd) {
	m.phases = m.buildPhases()
	if len(m.phases) == 0 {
		m.perr = "Nothing selected to delete."
		return m, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.runCtx = ctx
	m.phaseIdx = 0
	m.phaseResults = nil
	m.members, m.membersLoaded = nil, false // refetched once per run
	m.started = true
	m.startedAt = time.Now()
	m.reported = false
	m.stopping, m.pausePend = false, false
	m.reportPath, m.notifyResult, m.logWarn = "", "", ""
	m.reportHit, m.openErr = hitBox{}, ""
	m.screen = scRunning

	// Remote control (pause/resume/stop from the phone) rides the same ntfy
	// topic, on a derived control sub-topic, for the whole run.
	m.controlCh, m.controlOn = nil, false
	var cmds []tea.Cmd
	if target := resolveNtfyURL(m.cfg.ntfy); m.cfg.execute && target != "" {
		if ctl := controlTarget(target); ctl != "" {
			ch := make(chan controlCmd, 8)
			m.controlCh, m.controlOn = ch, true
			go subscribeControl(ctx, ctl, func(c controlCmd) {
				select {
				case ch <- c:
				case <-ctx.Done():
				}
			})
			cmds = append(cmds, waitControl(ch))
		}
	}
	cmds = append(cmds, m.startPhase(0))
	return m, tea.Batch(cmds...)
}

// startPhase spins up the engine for phase i, closing any previous phase's log.
func (m *appModel) startPhase(i int) tea.Cmd {
	p := m.phases[i]
	m.stats = NewStats(p.total, m.cfg.workers)
	m.rateHist = nil // each phase gets its own sparkline history
	if m.progLog != nil {
		m.progLog.close()
		m.progLog = nil
	}
	var onDeleted func(string)
	if m.cfg.execute && p.logPath != "" {
		if pl, err := openProgressLog(p.logPath); err == nil {
			m.progLog = pl
			onDeleted = pl.record
		} else {
			// executeGuard probed this log before the run, so this only happens
			// on mid-run breakage; warn and carry on.
			m.logWarn = "resume log unavailable (" + err.Error() + "): this phase's deletions will be re-attempted on the next run"
		}
	}
	minInterval := time.Duration(float64(time.Second) / clampFloat(m.cfg.maxRPS, 1, 49))
	m.eng = NewEngine(EngineConfig{
		Token:             strings.TrimSpace(m.cfg.token),
		Workers:           m.cfg.workers,
		DeleteDelay:       p.floor,
		Jitter:            m.cfg.jitter,
		DryRun:            !m.cfg.execute,
		GlobalMinInterval: minInterval,
		OnDeleted:         onDeleted,
		CF:                m.cfb,
	}, m.stats)
	m.paused = m.pausePend && m.cfg.execute
	m.pausePend = false
	m.eng.setPaused(m.paused)
	m.lastNotify = time.Now()
	go m.eng.Run(m.runCtx, p.jobs)
	if m.paused {
		// The pause was taken at the boundary with no engine to hold it; confirm
		// it to the phone now that one does.
		return tea.Batch(doTick(), m.progressNotifyCmd(true))
	}
	return doTick()
}

// noteLogErr surfaces the resume log's first write failure on the running
// screen. bufio's error is sticky: nothing is recorded after it, so those
// deletions repeat on the next run while this one keeps going.
func (m *appModel) noteLogErr() {
	if m.logWarn != "" || m.progLog == nil {
		return
	}
	if err := m.progLog.writeErr(); err != nil {
		m.logWarn = "resume log write failed (" + err.Error() + "): deletions from that point on will be re-attempted on the next run"
	}
}

// phaseResult captures a finished phase's outcome, resolving its channels to
// servers for the undeletable rollup.
func (m *appModel) phaseResult(snap Snapshot) opResult {
	kind := m.phases[m.phaseIdx].kind
	meta := metaFromRaws(m.raws)
	if kind == "reactions" {
		meta = metaFromReactions(m.reactions, m.guildNames)
	}
	return opResult{
		Kind:      kind,
		Snap:      snap,
		Collapsed: snap.ActiveLimit >= 1 && snap.ActiveLimit < len(snap.Workers),
		Forbidden: m.forbiddenServers(snap, meta),
	}
}

// controlMsg carries a remote command from the ntfy control topic into the
// Bubble Tea loop, so it's applied on the UI goroutine like a keypress.
type controlMsg struct{ cmd controlCmd }

// waitControl blocks on the control channel and delivers the next command. It's
// re-armed after each command so the stream keeps flowing while the run is live.
func waitControl(ch chan controlCmd) tea.Cmd {
	return func() tea.Msg {
		c, ok := <-ch
		if !ok {
			return nil
		}
		return controlMsg{cmd: c}
	}
}

// rearmControl re-issues the control listener, or nil if control isn't active.
func (m *appModel) rearmControl() tea.Cmd {
	if m.controlOn && m.controlCh != nil {
		return waitControl(m.controlCh)
	}
	return nil
}

// applyControl handles a remote pause/resume/stop, mirroring the p/stop hotkeys,
// and re-arms the listener so more commands arrive.
func (m *appModel) applyControl(c controlCmd) (tea.Model, tea.Cmd) {
	if m.screen != scRunning || m.eng == nil || m.reported {
		return m, m.rearmControl()
	}
	// Between a phase finishing and the tick that starts the next one there is
	// no live engine, so pause and stop are held for the next phase rather than
	// dropped: the run would otherwise keep deleting past a stop the user
	// believes took effect.
	boundary := m.atPhaseBoundary()
	var out tea.Cmd
	switch c {
	case cmdPause:
		switch {
		case !m.cfg.execute:
		case boundary:
			m.pausePend = true
		case !m.eng.isPaused():
			m.eng.setPaused(true)
			m.paused = true
			m.lastNotify = time.Now()
			out = m.progressNotifyCmd(true)
		}
	case cmdResume:
		switch {
		case !m.cfg.execute:
		case boundary:
			m.pausePend = false
		case m.eng.isPaused():
			m.eng.setPaused(false)
			m.paused = false
			m.lastNotify = time.Now()
			out = m.progressNotifyCmd(false)
		}
	case cmdStop:
		// Wind the run down; the finished tick writes the report and fires the
		// completion ping. The final frame stays until the user leaves.
		m.stopping = true
		if m.cancel != nil {
			m.cancel()
		}
	}
	return m, tea.Batch(out, m.rearmControl())
}

// atPhaseBoundary reports whether the phase's engine has wound down, leaving no
// live engine for a control command to act on.
func (m *appModel) atPhaseBoundary() bool {
	if m.stats == nil {
		return true
	}
	snap := m.stats.Snapshot()
	return snap.Finished || snap.Aborted
}

// maybePeriodicNotify fires a progress ntfy once the interval has elapsed.
func (m *appModel) maybePeriodicNotify() tea.Cmd {
	if !m.cfg.execute || m.cfg.notifyEvery <= 0 || resolveNtfyURL(m.cfg.ntfy) == "" {
		return nil
	}
	if time.Since(m.lastNotify) < m.cfg.notifyEvery {
		return nil
	}
	m.lastNotify = time.Now()
	return m.progressNotifyCmd(m.eng != nil && m.eng.isPaused())
}

// progressNotifyCmd posts a live progress/pause notification in the background.
// It reports nothing back (nil msg), so the final-frame notify result is left to
// the completion ping.
func (m *appModel) progressNotifyCmd(paused bool) tea.Cmd {
	target := resolveNtfyURL(m.cfg.ntfy)
	if target == "" || !m.cfg.execute || m.stats == nil {
		return nil
	}
	msg := runningNtfy(m.pkgName, m.stats.Snapshot(), paused, controlTarget(target))
	return func() tea.Msg {
		_ = postNtfy(context.Background(), target, msg)
		return nil
	}
}

// notifyExit sends a final "stopped" notification synchronously on a manual quit.
// tea.Quit won't run async commands, so this is bounded and best-effort:
// the quit never hangs on the network.
func (m *appModel) notifyExit() {
	target := resolveNtfyURL(m.cfg.ntfy)
	if target == "" || m.stats == nil {
		return
	}
	// Report finished phases plus the in-flight one under its real kind, so a
	// quit mid-reactions isn't pushed as "N messages deleted".
	results := append([]opResult{}, m.phaseResults...)
	kind := "messages"
	if m.phaseIdx >= 0 && m.phaseIdx < len(m.phases) {
		kind = m.phases[m.phaseIdx].kind
	}
	results = append(results, opResult{Kind: kind, Snap: m.stats.Snapshot()})
	r := runReport{
		Package:   m.pkgName,
		Execute:   m.cfg.execute,
		StartedAt: m.startedAt,
		EndedAt:   time.Now(),
		Results:   results,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_ = sendNtfy(ctx, target, r.notifyTitle(), r.notifyBody(), r.notifyPriority(), r.notifyTags())
}

// finalizeRun writes the end-of-run report and returns a command that fires the
// ntfy ping (async, so a slow network never freezes the final frame). Called
// exactly once, when the engine reports finished/aborted.
func (m *appModel) finalizeRun() tea.Cmd {
	r := runReport{
		Package:   m.pkgName,
		Execute:   m.cfg.execute,
		StartedAt: m.startedAt,
		EndedAt:   time.Now(),
		Results:   m.phaseResults,
		Resumed:   m.resumed,
	}
	cmds := []tea.Cmd{notifyCmd(resolveNtfyURL(m.cfg.ntfy), r)}
	if path := r.destPath(m.reportOverride, m.reportProgPath()); path != "" {
		if err := writeRunReport(path, r); err == nil {
			m.reportPath = path
			// Only now: during the run the terminal keeps the mouse, so
			// selecting text works as usual.
			cmds = append(cmds, tea.EnableMouseCellMotion)
		}
	}
	return tea.Batch(cmds...)
}

// clickReport answers a plain click on the final frame's button, which the OSC 8
// link cannot: terminals reserve that for ctrl/cmd+click.
func (m *appModel) clickReport(msg tea.MouseMsg) {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return
	}
	if m.reportPath != "" && m.reportHit.contains(msg.X, msg.Y) {
		m.openReport()
	}
}

func (m *appModel) openReport() {
	m.openErr = ""
	if err := openFile(m.reportPath); err != nil {
		m.openErr = "could not open " + m.reportPath + ": " + err.Error()
	}
}

// reportProgPath is the resume log the report path is derived from: the message
// log if a message phase ran, else the reaction log.
func (m *appModel) reportProgPath() string {
	if m.progPath != "" {
		return m.progPath
	}
	return m.reactProgPath
}

// forbiddenServers rolls undeletable (403) messages up to their servers for the
// report. When any exist on a real run, one guilds call (time-boxed) labels each
// server as left or still-joined; a dry run or a missing token skips the labels.
func (m *appModel) forbiddenServers(snap Snapshot, meta map[string]chanMeta) []forbiddenServer {
	if len(snap.Forbidden) == 0 {
		return nil
	}
	if tok := strings.TrimSpace(m.cfg.token); m.cfg.execute && tok != "" && !m.membersLoaded {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		m.members = fetchGuildMembership(ctx, tok)
		cancel()
		m.membersLoaded = true
	}
	return forbiddenByServer(snap.Forbidden, meta, m.members)
}

// notifyCmd posts the completion ping in the background, reporting the outcome
// back as a notifyDoneMsg.
func notifyCmd(target string, r runReport) tea.Cmd {
	if target == "" {
		return nil
	}
	return func() tea.Msg {
		err := sendNtfy(context.Background(), target, r.notifyTitle(), r.notifyBody(), r.notifyPriority(), r.notifyTags())
		return notifyDoneMsg{err: err}
	}
}

type notifyDoneMsg struct{ err error }

// finishRun closes the progress log and reloads the done-set so a subsequent
// run (or the returning home preview) reflects what this run deleted.
func (m *appModel) finishRun() {
	if m.progLog != nil {
		m.progLog.close()
		if err := m.progLog.writeErr(); err != nil && m.perr == "" {
			m.perr = "Resume log write failed (" + err.Error() + "). Deletions after the failure were not recorded and will be re-attempted on the next run."
		}
		m.progLog = nil
	}
	// A run only aborts on repeated 401, which means the token has almost
	// certainly rotated. Drop the stored copy, but only when the rejected token
	// is the stored one (savedToken tracks what the keyring holds): an abort on
	// a pasted or flag token says nothing about a good token in the store.
	tok := strings.TrimSpace(m.cfg.token)
	if m.stats != nil && m.stats.Snapshot().Aborted && m.stateKey != "" &&
		tok != "" && tok == m.savedToken && hasStoredToken(m.stateKey) {
		if err := forgetToken(m.stateKey); err == nil {
			m.savedToken, m.tokenFromStore = "", false
			m.perr = "The stored token was rejected (401) and has been forgotten. Re-authenticate before the next run."
		} else {
			m.perr = "The stored token was rejected (401) but could not be removed from the keyring: " + err.Error()
		}
	}
	if m.progPath != "" {
		m.done = loadProgressSet(m.progPath)
		m.resumed = countInSet(m.raws, m.done)
	}
	// Reload the reaction resume set too, so a second run in the session skips
	// what this one removed.
	if m.reactProgPath != "" {
		m.reactDone = loadProgressSet(m.reactProgPath)
	}
}

// --- confirm ---------------------------------------------------------------

func (m *appModel) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "y", "Y":
		// Re-run the execute guard: the async token probe may have resolved to
		// invalid or account-mismatched while this confirm screen was open, and
		// the pre-confirm check in startRun would then be stale.
		if reason := m.executeGuard(); reason != "" {
			m.perr = reason
			m.screen = scHome
			return m, nil
		}
		return m.launchEngine()
	case "n", "N", "esc", "q":
		m.screen = scHome
		return m, nil
	}
	return m, nil
}

func (m *appModel) viewConfirm() string {
	var b strings.Builder
	b.WriteString(appHeader(m.width, "confirm deletion", modeBadge(true)) + "\n\n")

	var parts []string
	if m.cfg.delMessages && m.total > 0 {
		parts = append(parts, stValue.Render(commafy(m.total))+stRed.Render(" message(s)"))
	}
	if m.cfg.delReactions && m.reactTotal > 0 {
		parts = append(parts, stValue.Render(commafy(m.reactTotal))+stRed.Render(" reaction(s)"))
	}
	body := strings.Join([]string{
		stRed.Render("This permanently removes ") + strings.Join(parts, stDim.Render(" and ")) + stRed.Render("."),
		stDim.Render("Irreversible. Deletes are logged progressively. A stop or crash resumes where you left off."),
	}, "\n\n")
	b.WriteString(panel("Permanently delete?", body, m.width, nord11) + "\n")

	b.WriteString("  " + button("y: delete for real", nord11) + "   " + button("n: cancel", nord8) + "\n")
	b.WriteString(wrapText(stKeyHelp.Render("y confirm · n/esc cancel"), m.width, 2) + "\n")
	return b.String()
}

// --- running ---------------------------------------------------------------

func (m *appModel) updateRunning(msg tea.Msg) (tea.Model, tea.Cmd) {
	if mouse, ok := msg.(tea.MouseMsg); ok {
		m.clickReport(mouse)
		return m, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	snap := m.stats.Snapshot()
	done := snap.Finished || snap.Aborted
	switch key.String() {
	case "q", "ctrl+c":
		if m.cancel != nil {
			m.cancel()
		}
		m.finishRun()
		// "any exit" gets a ping too, but only if the run didn't already finalize
		// one (finished/aborted), otherwise it's a duplicate.
		if !m.reported {
			m.notifyExit()
		}
		m.quitting = true
		return m, tea.Quit
	case "b", "esc":
		if done {
			if m.cancel != nil {
				m.cancel()
			}
			m.finishRun()
			m.recompute()
			m.screen = scHome
			// Hand the mouse back: nothing off this screen is clickable.
			m.reportHit, m.openErr = hitBox{}, ""
			return m, tea.DisableMouse
		}
	case "o":
		if m.reportPath != "" { // only set once the run has finalized
			m.openReport()
		}
	case "p", " ":
		// Pause / resume (no-op once the run is done or in dry run). A manual
		// pause/resume also pushes an ntfy so a watching phone stays in sync.
		if !done && m.eng != nil && m.cfg.execute {
			m.paused = m.eng.togglePaused()
			m.lastNotify = time.Now()
			return m, m.progressNotifyCmd(m.paused)
		}
	case "[", "{":
		// Slower: widen the per-channel spacing floor.
		if !done && m.eng != nil {
			m.eng.nudgeDelay(200 * time.Millisecond)
		}
	case "]", "}":
		// Faster: tighten the floor (down to 0 = adaptive-only).
		if !done && m.eng != nil {
			m.eng.nudgeDelay(-200 * time.Millisecond)
		}
	}
	return m, nil
}

func (m *appModel) viewRunning() string {
	snap := m.stats.Snapshot()
	var b strings.Builder

	kind, noun := "messages", "messages"
	if m.phaseIdx < len(m.phases) {
		kind = m.phases[m.phaseIdx].kind
		noun = kind
	}
	sub := "removing " + kind
	if !m.cfg.execute {
		sub = "dry run: " + kind
	}
	if len(m.phases) > 1 {
		sub += fmt.Sprintf(" (phase %d/%d)", m.phaseIdx+1, len(m.phases))
	}
	b.WriteString(appHeader(m.width, sub, modeBadge(m.cfg.execute)) + "\n\n")

	frac := 0.0
	if snap.Total > 0 {
		frac = float64(snap.Processed) / float64(snap.Total)
	}
	prog := m.prog.ViewAs(frac) + "  " + stValue.Render(fmt.Sprintf("%5.1f%%", frac*100)) + "\n" +
		stDim.Render(fmt.Sprintf("%s of %s %s", commafy(int(snap.Processed)), commafy(int(snap.Total)), noun))
	b.WriteString(panel("Progress", prog, m.width, nord8) + "\n")

	col := colWidth(m.width)
	b.WriteString(twoCol(m.width,
		panel("Tally", m.tallyBody(snap), col, nord9),
		panel("Rate", m.rateBody(snap, col), col, nord9)) + "\n")

	b.WriteString(panel("Workers", m.workersBody(snap), m.width, nord7) + "\n")

	// Persistent explanation when throttling has collapsed us to fewer workers,
	// so the idle workers don't read as "stuck".
	if n := len(snap.Workers); snap.ActiveLimit >= 1 && snap.ActiveLimit < n {
		note := fmt.Sprintf("account-wide rate limit: running %d of %d workers. Old messages detected; Discord rate limits are harsh. Extra workers not needed.", snap.ActiveLimit, n)
		b.WriteString(wrapText(stYellow.Render(note), m.width, 2) + "\n")
	}

	if m.logWarn != "" {
		b.WriteString(wrapText(stYellow.Render("⚠ "+m.logWarn), m.width, 2) + "\n")
	}

	if len(snap.Errors) > 0 {
		b.WriteString(panel("Recent errors", m.errorsBody(snap), m.width, nord11) + "\n")
	}

	if snap.Status != "" {
		st := stYellow
		if snap.Aborted {
			st = stRed
		}
		b.WriteString(wrapText(st.Render("● "+snap.Status), m.width, 2) + "\n")
	}

	if snap.Finished || snap.Aborted {
		if n := forbiddenTotal(snap); n > 0 {
			b.WriteString(wrapText(stYellow.Render(fmt.Sprintf("⤼ %s message(s) can't be deleted (system messages or servers/DMs you've left). Post-run report has more details.", commafy(n))), m.width, 2) + "\n")
		}
		if m.reportPath != "" {
			// Wrapped first, linked after: the escapes stay out of the wrap.
			b.WriteString(linkPath(m.reportPath,
				wrapText(stDim.Render("report: "+m.reportPath), m.width, 2)) + "\n")
			line, hit := reportButton(m.reportPath, strings.Count(b.String(), "\n"))
			m.reportHit = hit
			b.WriteString(line + "\n")
			if m.openErr != "" {
				b.WriteString(wrapText(stYellow.Render("⚠ "+m.openErr), m.width, 2) + "\n")
			}
		}
		if m.notifyResult != "" {
			b.WriteString(wrapText(stDim.Render(m.notifyResult), m.width, 2) + "\n")
		}
		done := stGreen.Render("done")
		switch {
		case snap.Aborted:
			done = stRed.Render("aborted")
		case !snap.Completed:
			done = stYellow.Render("stopped")
		}
		keys := ""
		if m.reportPath != "" {
			keys = stKeyHelp.Render("o") + stDim.Render(" open report  ")
		}
		b.WriteString(wrapText(done+stDim.Render("  ·  ")+keys+
			stKeyHelp.Render("b")+stDim.Render(" home  ")+stKeyHelp.Render("q")+stDim.Render(" quit"), m.width, 2) + "\n")
	} else {
		if m.paused {
			b.WriteString(wrapText(stYellow.Render("⏸ PAUSED · press p to resume"), m.width, 2) + "\n")
		}
		// Live pacing floor + Cloudflare invalid-response budget.
		info := "pacing floor " + stValue.Render(fmtPace(m.engBaseDelay()))
		if snap.InvalidWindow > 0 {
			info += stDim.Render("  ·  invalid budget ") + stValue.Render(fmt.Sprintf("%s / %s", commafy(snap.InvalidWindow), commafy(cfHardLimit)))
		}
		b.WriteString(wrapText(stDim.Render(info), m.width, 2) + "\n")
		help := "q/ctrl+c stop (resumable)"
		if m.cfg.execute {
			help = "p pause · [ slower · ] faster · " + help
		} else {
			help = "[ slower · ] faster · " + help
		}
		b.WriteString(wrapText(stKeyHelp.Render(help), m.width, 2) + "\n")
	}

	out := b.String()
	// Bubble Tea paints only the last m.height lines, so the button moves up with
	// whatever scrolled off the top.
	if lines := strings.Count(out, "\n") + 1; m.height > 0 && lines > m.height {
		m.reportHit.y -= lines - m.height
	}
	return out
}

// engBaseDelay is the current live pacing floor (0 if the engine isn't running).
func (m *appModel) engBaseDelay() time.Duration {
	if m.eng == nil {
		return time.Duration(m.cfg.delay * float64(time.Second))
	}
	return m.eng.baseDelay()
}

// fmtPace renders a spacing floor, calling out when adaptive pacing is off.
func fmtPace(d time.Duration) string {
	if d <= 0 {
		return "off (adaptive only)"
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func (m *appModel) tallyBody(snap Snapshot) string {
	return strings.Join([]string{
		kv("✓ deleted", 10, stGreen.Render(commafy(int(snap.Deleted)))),
		kv("⤼ skipped", 10, stYellow.Render(commafy(int(snap.Skipped)))),
		kv("✗ failed", 10, stOrange.Render(commafy(int(snap.Failed)))),
		kv("elapsed", 10, stFrost.Render(fmtDur(snap.Elapsed))),
		kv("eta", 10, stFrost.Render(etaStr(snap))),
	}, "\n")
}

func (m *appModel) rateBody(snap Snapshot, col int) string {
	spark := sparkline(m.rateHist, col-4, nord14)
	errFrac := 0.0
	if snap.Processed > 0 {
		errFrac = float64(snap.Failed) / float64(snap.Processed)
	}
	return strings.Join([]string{
		stGreen.Render(fmt.Sprintf("%.2f", snap.Rate)) + stDim.Render(" msg/s now"),
		spark,
		kv("errors", 8, stRed.Render(fmt.Sprintf("%.1f%%", errFrac*100))),
	}, "\n")
}

func (m *appModel) workersBody(snap Snapshot) string {
	var wb strings.Builder
	for i, w := range snap.Workers {
		id := stDim.Render(fmt.Sprintf("%d", i))
		if !w.Active {
			fmt.Fprintf(&wb, "%s %s\n", id, stDim.Render("idle"))
			continue
		}
		fmt.Fprintf(&wb, "%s %s %s\n", id, miniBar(w.Done, w.Total, 14),
			truncate(w.Channel, clampInt(m.width-34, 12, 60)))
	}
	return strings.TrimRight(wb.String(), "\n")
}

func (m *appModel) errorsBody(snap Snapshot) string {
	var eb strings.Builder
	start := 0
	if len(snap.Errors) > 5 {
		start = len(snap.Errors) - 5
	}
	for _, e := range snap.Errors[start:] {
		eb.WriteString(stRed.Render("• "+truncate(e, clampInt(m.width-12, 20, 100))) + "\n")
	}
	return strings.TrimRight(eb.String(), "\n")
}

// Helper reused by home/configure headers.
func modeBadge(execute bool) string {
	if execute {
		return badge("EXECUTE", nord12)
	}
	return badge("DRY RUN", nord13)
}

// --- home view -------------------------------------------------------------

func (m *appModel) viewHome() string {
	var b strings.Builder
	b.WriteString(appHeader(m.width, "bulk message deletion", modeBadge(m.cfg.execute)) + "\n\n")

	col := colWidth(m.width)

	b.WriteString(twoCol(m.width,
		panel("Target", m.targetBody(), col, nord8),
		panel("Plan", m.planBody(), col, nord8)) + "\n")

	b.WriteString(panel("Selection", m.selectionBody(m.width-4), m.width, nord14) + "\n")

	b.WriteString(twoCol(m.width,
		panel("Filters", m.filtersBody(), col, nord9),
		panel("Account", m.accountBody(), col, nord9)) + "\n")

	if m.ownerMismatch() {
		b.WriteString(wrapText(stRed.Render("✗ "+m.mismatchNote()), m.width, 2) + "\n")
	}
	if m.perr != "" {
		b.WriteString(wrapText(stRed.Render("! "+m.perr), m.width, 2) + "\n")
	}

	// Actions + footer
	start := button("▶ Start dry run", nord14)
	if m.cfg.execute {
		start = button("▶ Start DELETING", nord11)
	}
	b.WriteString("\n  " + start + "   " + button("⚙ Configure", nord8) + "\n")
	b.WriteString(wrapText(stKeyHelp.Render("enter/s start · c configure · e dry-run/execute · q quit"), m.width, 2) + "\n")
	if m.updateLine != "" {
		b.WriteString(wrapText(stDim.Render(m.updateLine), m.width, 2) + "\n")
	}
	return b.String()
}

// --- home panel bodies ---

func (m *appModel) targetBody() string {
	nSel, nTot := m.selectionCounts()
	chans := stValue.Render(commafy(nSel))
	if nSel == nTot {
		chans += stDim.Render(" / " + commafy(nTot) + " (all)")
	} else {
		chans += stDim.Render(" / " + commafy(nTot) + " selected")
	}
	msgVal := stValue.Render(commafy(m.total)) + stLabel.Render(" messages")
	if !m.cfg.delMessages {
		msgVal = stDim.Render("messages off")
	}
	rows := []string{
		kv("to delete", 10, msgVal),
		kv("channels", 10, chans),
		kv("package", 10, stDim.Render(commafy(m.packageTotal())+" total")),
	}
	if m.caps.HasReactions {
		rval := stValue.Render(commafy(m.reactTotal)) + stLabel.Render(" reactions")
		if !m.cfg.delReactions {
			rval = stDim.Render(commafy(m.reactTotal) + " reactions (off)")
		}
		rows = append(rows, kv("reactions", 10, rval))
	}
	if m.resumed > 0 {
		rows = append(rows, kv("resumed", 10, stFrost.Render(commafy(m.resumed))+stDim.Render(" already done, skipped")))
	}
	rows = append(rows, kv("source", 10, stDim.Render(truncate(m.pkgName, clampInt(colWidth(m.width)-18, 8, 40)))))
	return strings.Join(rows, "\n")
}

func (m *appModel) planBody() string {
	tp := m.cfg.maxRPS
	if m.cfg.delay > 0 {
		if t := float64(m.cfg.workers) / m.cfg.delay; t < tp {
			tp = t
		}
	}
	return strings.Join([]string{
		kv("est. time", 11, stFrost.Render(fmtDur(m.previewETA()))),
		kv("throughput", 11, stFrost.Render(fmt.Sprintf("~%.1f msg/s", tp))),
		kv("workers", 11, stValue.Render(fmt.Sprint(m.cfg.workers))),
		kv("pace", 11, stDim.Render(fmt.Sprintf("~%.1fs / delete", m.cfg.delay))),
	}, "\n")
}

func (m *appModel) selectionBody(width int) string {
	pkg := m.packageTotal()
	frac := 0.0
	if pkg > 0 {
		frac = float64(m.total) / float64(pkg)
	}
	barW := clampInt(width-26, 10, 80)
	label := fmt.Sprintf("  %s%s", stValue.Render(commafy(m.total)),
		stDim.Render(fmt.Sprintf(" / %s  (%.0f%%)", commafy(pkg), frac*100)))
	return miniBar(m.total, pkg, barW) + label
}

func (m *appModel) filtersBody() string {
	dates := "any"
	bounds, _ := resolveBounds(m.cfg.afterSnow, m.cfg.beforeSnow, m.cfg.afterDate, m.cfg.beforeDate, m.cfg.last, time.Now())
	const df = "2006-01-02"
	switch {
	case bounds.AfterID != 0 && bounds.BeforeID != 0:
		dates = snowflakeToTime(bounds.AfterID).Format(df) + " → " + snowflakeToTime(bounds.BeforeID).Format(df)
	case bounds.AfterID != 0:
		dates = "after " + snowflakeToTime(bounds.AfterID).Format(df)
	case bounds.BeforeID != 0:
		dates = "before " + snowflakeToTime(bounds.BeforeID).Format(df)
	}
	valW := clampInt(colWidth(m.width)-16, 8, 44)
	content := "any"
	if strings.TrimSpace(m.cfg.content) != "" {
		content = "\"" + m.cfg.content + "\""
	}
	order := "oldest first"
	if m.cfg.order == "newest" {
		order = "newest first"
	}
	return strings.Join([]string{
		kv("order", 9, stFrost.Render(order)),
		kv("type", 9, stFrost.Render(truncate(typeSelSummary(m.cfg.typeSel), valW))),
		kv("dates", 9, stFrost.Render(truncate(dates, valW))),
		kv("contains", 9, stFrost.Render(truncate(content, valW))),
	}, "\n")
}

func (m *appModel) accountBody() string {
	valW := clampInt(colWidth(m.width)-16, 6, 34)
	mode := stYellow.Render("DRY RUN")
	if m.cfg.execute {
		mode = stGreen.Render("EXECUTE")
	}

	tok := stDim.Render("not set")
	switch {
	case m.browserActive:
		tok = stFrost.Render("opening browser…")
	case m.browserErr != "":
		tok = stYellow.Render("sign-in failed")
	default:
		switch m.tokenState {
		case tsChecking:
			tok = stFrost.Render("checking…")
		case tsValid:
			if m.ownerMismatch() {
				tok = stRed.Render("✗ wrong account")
			} else {
				tok = stGreen.Render("✓ " + truncate(m.tokenUser, valW))
			}
		case tsInvalid:
			tok = stRed.Render("✗ invalid")
		case tsError:
			tok = stYellow.Render("set (unverified)")
		default:
			if strings.TrimSpace(m.cfg.token) != "" {
				tok = stFrost.Render("set")
			}
		}
	}

	var rows []string
	if m.ownerName != "" {
		rows = append(rows, kv("owner", 7, stFrost.Render(truncate(m.ownerName, valW))))
	}
	rows = append(rows, kv("token", 7, tok), kv("mode", 7, mode))
	return strings.Join(rows, "\n")
}

// --- guild grouping --------------------------------------------------------

func (m *appModel) buildGuilds() {
	m.guilds = groupGuilds(m.raws)
}

// groupGuilds buckets channels under their server (or the synthetic DM group),
// with chans holding indices into raws. Used for both the message and reaction
// channel selectors.
func groupGuilds(raws []RawChannel) []guildGroup {
	idx := map[string]int{}
	var groups []guildGroup
	get := func(id, name string, isDM bool) int {
		if i, ok := idx[id]; ok {
			return i
		}
		groups = append(groups, guildGroup{id: id, name: name, isDM: isDM})
		idx[id] = len(groups) - 1
		return len(groups) - 1
	}
	for ci, rc := range raws {
		var gi int
		if rc.IsDM {
			gi = get("", "Direct Messages", true)
		} else {
			name := rc.GuildName
			if name == "" {
				name = "Server " + rc.GuildID
			}
			gi = get(rc.GuildID, name, false)
		}
		groups[gi].chans = append(groups[gi].chans, ci)
		groups[gi].msgSum += rc.items()
	}
	sortGuilds(groups) // servers first (alpha), DMs last
	return groups
}

// packageTotal is the number of messages across the whole package before any
// filtering: the denominator for the Selection bar.
func (m *appModel) packageTotal() int {
	n := 0
	for _, rc := range m.raws {
		n += len(rc.Messages)
	}
	return n
}

func (m *appModel) selectionCounts() (selected, total int) {
	return countSelected(m.raws, m.selected)
}
