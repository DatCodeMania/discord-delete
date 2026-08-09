package main

import (
	"cmp"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type cfKind int

const (
	cfToggle cfKind = iota
	cfText
	cfAction
	cfExpander
)

type cfField struct {
	id     string
	label  string
	help   string
	kind   cfKind
	masked bool
}

var primaryFields = []cfField{
	{id: "order", label: "Order", kind: cfToggle,
		help: "Order by which messages are deleted."},
	{id: "content", label: "Contains text", kind: cfText,
		help: "Only delete messages whose text contains this (case-insensitive). Wrap in slashes for a regex: /pattern/ or /pattern/i. Blank = disabled."},
	{id: "types", label: "Message type", kind: cfAction,
		help: "Filter by what a message contains: text, media (and subtypes like image/video/voice), or links. Multi-select; none = all types. Enter opens the selector."},
	{id: "afterDate", label: "After date", kind: cfText,
		help: "Only messages sent on/after this date. Format YYYY-MM-DD (or RFC3339). Blank = no lower bound."},
	{id: "beforeDate", label: "Before date", kind: cfText,
		help: "Only messages sent before this date. Format YYYY-MM-DD (or RFC3339). Blank = no upper bound."},
	{id: "last", label: "Within last", kind: cfText,
		help: "Quick lower bound: 7d, 2w, 3mo, 1y, day/week/month/year, or today (since midnight). If it overlaps after date, the tighter one wins."},
	{id: "channels", label: "Channels", kind: cfAction,
		help: "Pick which servers and DMs to include. Enter opens the selector."},
	{id: "token", label: "Token", kind: cfText, masked: true,
		help: "Your Discord user token, required only to actually delete. Obtain via Browser sign-in or manually."},
	{id: "browser", label: "Browser sign-in", kind: cfAction,
		help: "Launch a browser (Chrome/Chromium/Edge/Brave) and log into Discord normally; your token is captured automatically. Throwaway profile. Requires a Chromium-based browser."},
	{id: "remember", label: "Remember token", kind: cfToggle,
		help: "Off by default. On: saves a validated token to your OS keyring, encrypted at rest. No keyring = memory only. Off forgets the stored token."},
	{id: "forget", label: "Forget token", kind: cfAction,
		help: "Delete any token stored for this account from the keyring."},
	{id: "ntfy", label: "Notify (ntfy)", kind: cfText,
		help: "Ping this ntfy topic (or full URL for a self-hosted server) periodically (below) and when the run finishes. Entering a topic fires a test ping. Blank = disabled."},
	{id: "notifyEvery", label: "Notify every", kind: cfText,
		help: "Push a progress update to ntfy this often (30m, 1h). Requires the ntfy field set."},
	{id: "execute", label: "Mode", kind: cfToggle,
		help: "Dry run previews without deleting anything. Execute permanently deletes, and needs a token."},
	{id: "reset", label: "Reset defaults", kind: cfAction,
		help: "Restore all options to their defaults. Your token is kept."},
}

var advancedFields = []cfField{
	{id: "workers", label: "Workers", kind: cfText,
		help: "How many channels are deleted in parallel. Each channel still runs serially and human-paced."},
	{id: "delay", label: "Delay (s)", kind: cfText,
		help: "Fastest spacing between deletes in one channel; a floor. The tool slows above it when rate-limited, never below. 0 = no pacing (not recommended)."},
	{id: "jitter", label: "Jitter", kind: cfText,
		help: "Random +/- fraction applied to Delay so the timing isn't perfectly regular."},
	{id: "maxRPS", label: "Max req/s", kind: cfText,
		help: "Hard account-wide request ceiling, well under Discord's 50/s global limit."},
	{id: "reactionDelay", label: "React delay (s)", kind: cfText,
		help: "Fastest spacing between reaction removals in one channel; a floor. Reaction rate-limit buckets are per-channel, so it can sit lower than for messages."},
}

var advExpander = cfField{id: "advanced", label: "Advanced", kind: cfExpander,
	help: "Performance and pacing knobs. Tuned to balance runtime and reliability, so change them at your own risk."}

// msgOnlyFields are filters that only make sense for message deletion; they are
// hidden when the package has no messages or messages aren't a delete target.
var msgOnlyFields = map[string]bool{"content": true, "types": true, "channels": true}

func (m *appModel) visibleFields() []cfField {
	var fs []cfField
	// Delete-target toggles come first, only when the package offers the choice.
	if m.caps.HasMessages {
		fs = append(fs, cfField{id: "delMessages", label: "Delete messages", kind: cfToggle,
			help: "Whether this run deletes your messages."})
	}
	if m.caps.HasReactions {
		fs = append(fs, cfField{id: "delReactions", label: "Delete reactions", kind: cfToggle,
			help: "Whether this run removes your reactions. Reactions share the date and order filters; scope their channels with the Reaction channels option."})
	}
	reactChannels := cfField{id: "reactChannels", label: "Reaction channels", kind: cfAction,
		help: "Pick which servers and DMs to remove reactions from. Enter opens the selector."}
	wantReactChannels := m.caps.HasReactions && m.cfg.delReactions
	for _, f := range primaryFields {
		if msgOnlyFields[f.id] && (!m.caps.HasMessages || !m.cfg.delMessages) {
			// The message channel picker is hidden here; still surface the reaction
			// channel picker in its place when reactions are a target.
			if f.id == "channels" && wantReactChannels {
				fs = append(fs, reactChannels)
			}
			continue
		}
		fs = append(fs, f)
		if f.id == "channels" && wantReactChannels {
			fs = append(fs, reactChannels)
		}
	}
	if m.caps.HasReactions && m.cfg.delReactions && m.caps.HasMessages && m.cfg.delMessages {
		fs = append(fs, cfField{id: "runOrder", label: "Run order", kind: cfToggle,
			help: "Which phase runs first when deleting both."})
	}
	fs = append(fs, advExpander)
	if m.advanced {
		for _, f := range advancedFields {
			// The reaction pacing knob only applies when the package has reactions.
			if f.id == "reactionDelay" && !m.caps.HasReactions {
				continue
			}
			fs = append(fs, f)
		}
	}
	return fs
}

// onOffCount renders a toggle's state with the count it governs.
func onOffCount(on bool, n int, noun string) string {
	s := "off"
	if on {
		s = "on"
	}
	return fmt.Sprintf("%s (%s %s)", s, commafy(n), noun)
}

func (m *appModel) fieldValue(id string) string {
	switch id {
	case "delMessages":
		return onOffCount(m.cfg.delMessages, m.total, "messages")
	case "delReactions":
		return onOffCount(m.cfg.delReactions, m.reactTotal, "reactions")
	case "runOrder":
		if m.cfg.reactionsFirst {
			return "reactions first"
		}
		return "messages first"
	case "order":
		if m.cfg.order == "newest" {
			return "newest first"
		}
		return "oldest first"
	case "content":
		return orPlaceholder(m.cfg.content, "any")
	case "types":
		return typeSelSummary(m.cfg.typeSel)
	case "afterDate":
		return orPlaceholder(m.cfg.afterDate, "any")
	case "beforeDate":
		return orPlaceholder(m.cfg.beforeDate, "any")
	case "last":
		return orPlaceholder(m.cfg.last, "any")
	case "channels":
		sel, tot := m.selectionCounts()
		if sel == tot {
			return "all (" + commafy(tot) + ")"
		}
		return commafy(sel) + " of " + commafy(tot)
	case "reactChannels":
		sel, tot := m.reactSelectionCounts()
		if sel == tot {
			return "all (" + commafy(tot) + ")"
		}
		return commafy(sel) + " of " + commafy(tot)
	case "browser":
		switch {
		case m.browserActive:
			return "opening browser… (esc cancels)"
		case m.browserErr != "":
			return "failed, see guide"
		default:
			return "launch to sign in"
		}
	case "reset":
		return "↩ restore defaults"
	case "ntfy":
		return orPlaceholder(m.cfg.ntfy, "off")
	case "notifyEvery":
		return fmtNotifyEvery(m.cfg.notifyEvery)
	case "token":
		if strings.TrimSpace(m.cfg.token) == "" {
			return "not set"
		}
		switch m.tokenState {
		case tsChecking:
			return "•••••••• checking…"
		case tsValid:
			return "•••••••• ✓ " + cmp.Or(m.tokenHandle, m.tokenUser)
		case tsInvalid:
			return "•••••••• ✗ invalid"
		case tsError:
			return "•••••••• set (unverified)"
		}
		return "•••••••• set"
	case "remember":
		if m.cfg.remember {
			return "on (saved for this account)"
		}
		if m.stateKey != "" && hasStoredToken(m.stateKey) {
			return "off (a stored token remains)"
		}
		return "off"
	case "forget":
		if m.stateKey != "" && hasStoredToken(m.stateKey) {
			return "↩ delete stored token"
		}
		return "nothing stored"
	case "execute":
		if m.cfg.execute {
			return "EXECUTE (deletes for real)"
		}
		return "dry run"
	case "workers":
		return strconv.Itoa(m.cfg.workers)
	case "delay":
		return fmt.Sprintf("%.2f", m.cfg.delay)
	case "jitter":
		return fmt.Sprintf("%.2f", m.cfg.jitter)
	case "maxRPS":
		return fmt.Sprintf("%.1f", m.cfg.maxRPS)
	case "reactionDelay":
		return fmt.Sprintf("%.2f", m.cfg.reactionDelay)
	}
	return ""
}

func orPlaceholder(s, ph string) string {
	if strings.TrimSpace(s) == "" {
		return ph
	}
	return s
}

func (m *appModel) updateConfigure(msg tea.Msg) (tea.Model, tea.Cmd) {
	fields := m.visibleFields()
	if m.fcursor >= len(fields) {
		m.fcursor = len(fields) - 1
	}
	if m.fcursor < 0 {
		m.fcursor = 0
	}

	// While a browser sign-in is in flight, the only interaction is cancelling
	// it; everything else is swallowed so the form can't be edited mid-capture.
	if m.browserActive {
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "esc", "ctrl+c":
				if m.browserCancel != nil {
					m.browserCancel()
				}
			}
		}
		return m, nil
	}

	if m.editing {
		key, ok := msg.(tea.KeyMsg)
		if ok {
			switch key.String() {
			case "enter":
				id := fields[m.fcursor].id
				m.commitEdit(id, m.input.Value())
				m.editing = false
				m.input.Blur()
				if id == "token" {
					return m, m.startTokenCheck()
				}
				if id == "ntfy" {
					return m, m.startNtfyTest()
				}
				return m, nil
			case "esc":
				m.editing = false
				m.input.Blur()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "esc", "b", "q":
		m.recompute()
		m.screen = scHome
		return m, nil
	case "up", "k":
		if m.fcursor > 0 {
			m.fcursor--
		}
		return m, nil
	case "down", "j":
		if m.fcursor < len(fields)-1 {
			m.fcursor++
		}
		return m, nil
	case "left", "h":
		if fields[m.fcursor].id == "order" {
			m.cfg.order = "oldest"
			m.recompute()
		}
		return m, nil
	case "right", "l":
		if fields[m.fcursor].id == "order" {
			m.cfg.order = "newest"
			m.recompute()
		}
		return m, nil
	case "enter", " ":
		return m.activateField(fields[m.fcursor])
	}
	return m, nil
}

func (m *appModel) activateField(f cfField) (tea.Model, tea.Cmd) {
	switch f.kind {
	case cfToggle:
		switch f.id {
		case "delMessages":
			m.cfg.delMessages = !m.cfg.delMessages
			m.recompute()
		case "delReactions":
			m.cfg.delReactions = !m.cfg.delReactions
			m.recompute()
		case "runOrder":
			m.cfg.reactionsFirst = !m.cfg.reactionsFirst
		case "order":
			if m.cfg.order == "newest" {
				m.cfg.order = "oldest"
			} else {
				m.cfg.order = "newest"
			}
			m.recompute()
		case "execute":
			m.toggleExecute()
		case "remember":
			m.cfg.remember = !m.cfg.remember
			if m.cfg.remember {
				m.maybeSaveToken()
			} else {
				m.forgetStoredToken()
			}
		}
		return m, nil
	case cfExpander:
		m.advanced = !m.advanced
		return m, nil
	case cfAction:
		switch f.id {
		case "channels":
			m.enterChannels(scopeMessages)
		case "reactChannels":
			m.enterChannels(scopeReactions)
		case "types":
			m.tcursor = 0
			m.screen = scTypes
		case "browser":
			if !m.browserActive {
				return m, m.startBrowserSignin()
			}
		case "reset":
			m.resetDefaults()
		case "forget":
			m.forgetStoredToken()
		}
		return m, nil
	case cfText:
		m.editing = true
		m.input.SetValue(m.currentText(f.id))
		m.input.Width = clampInt(m.width-24, 10, 60)
		if f.masked {
			m.input.EchoMode = textinput.EchoPassword
		} else {
			m.input.EchoMode = textinput.EchoNormal
		}
		m.input.CursorEnd()
		m.input.Focus()
		return m, nil
	}
	return m, nil
}

func (m *appModel) currentText(id string) string {
	switch id {
	case "content":
		return m.cfg.content
	case "afterDate":
		return m.cfg.afterDate
	case "beforeDate":
		return m.cfg.beforeDate
	case "last":
		return m.cfg.last
	case "ntfy":
		return m.cfg.ntfy
	case "notifyEvery":
		return fmtNotifyEvery(m.cfg.notifyEvery)
	case "token":
		return m.cfg.token
	case "workers":
		return strconv.Itoa(m.cfg.workers)
	case "delay":
		return fmt.Sprintf("%.2f", m.cfg.delay)
	case "jitter":
		return fmt.Sprintf("%.2f", m.cfg.jitter)
	case "maxRPS":
		return fmt.Sprintf("%.1f", m.cfg.maxRPS)
	case "reactionDelay":
		return fmt.Sprintf("%.2f", m.cfg.reactionDelay)
	}
	return ""
}

func (m *appModel) commitEdit(id, val string) {
	val = strings.TrimSpace(val)
	m.perr = ""
	switch id {
	case "content":
		m.cfg.content = val
	case "afterDate":
		m.cfg.afterDate = val
	case "beforeDate":
		m.cfg.beforeDate = val
	case "last":
		m.cfg.last = val
	case "ntfy":
		m.cfg.ntfy = val
	case "notifyEvery":
		if d, err := parseNotifyEvery(val); err == nil {
			m.cfg.notifyEvery = d
		} else {
			m.perr = err.Error()
		}
	case "token":
		m.cfg.token = normalizeToken(val)
	case "workers":
		if n, err := strconv.Atoi(val); err == nil {
			m.cfg.workers = clampInt(n, 1, 64)
		} else {
			m.perr = "workers must be a whole number"
		}
	case "delay":
		if f, err := strconv.ParseFloat(val, 64); err == nil && f >= 0 {
			m.cfg.delay = f
		} else {
			m.perr = "delay must be a number ≥ 0"
		}
	case "jitter":
		if f, err := strconv.ParseFloat(val, 64); err == nil && f >= 0 {
			m.cfg.jitter = f
		} else {
			m.perr = "jitter must be a number ≥ 0"
		}
	case "maxRPS":
		if f, err := strconv.ParseFloat(val, 64); err == nil && f > 0 {
			m.cfg.maxRPS = clampFloat(f, 1, 49)
		} else {
			m.perr = "max req/s must be a positive number"
		}
	case "reactionDelay":
		if f, err := strconv.ParseFloat(val, 64); err == nil && f >= 0 {
			m.cfg.reactionDelay = f
		} else {
			m.perr = "reaction delay must be a number ≥ 0"
		}
	}
	// recompute clears perr on a clean parse, which would hide this edit's
	// error; keep it.
	editErr := m.perr
	m.recompute()
	if editErr != "" {
		m.perr = editErr
	}
}

func (m *appModel) viewConfigure() string {
	fields := m.visibleFields()
	if m.fcursor >= len(fields) {
		m.fcursor = len(fields) - 1
	}
	if m.fcursor < 0 {
		m.fcursor = 0
	}
	var b strings.Builder
	b.WriteString(appHeader(m.width, "configure", modeBadge(m.cfg.execute)) + "\n\n")

	title := fmt.Sprintf("Settings  ·  %s msgs / %s channels", commafy(m.total), commafy(len(m.jobs)))
	b.WriteString(panel(title, m.settingsBody(m.width-4), m.width, nord8) + "\n")
	b.WriteString(panel("Guide", m.guideBody(fields[m.fcursor]), m.width, nord9) + "\n")

	switch {
	case m.browserActive:
		b.WriteString(wrapText(stKeyHelp.Render("waiting for browser sign-in… · esc cancel"), m.width, 2) + "\n")
	case m.editing:
		b.WriteString(wrapText(stKeyHelp.Render("enter save · esc cancel · ctrl+v paste"), m.width, 2) + "\n")
	default:
		b.WriteString(wrapText(stKeyHelp.Render("↑/↓ move · enter edit/toggle · ←/→ order · esc back to home"), m.width, 2) + "\n")
	}
	return b.String()
}

func (m *appModel) settingsBody(innerW int) string {
	fields := m.visibleFields()
	valW := clampInt(innerW-19, 8, 80)
	var b strings.Builder
	for i, f := range fields {
		cursor := "  "
		label := stLabel.Render(fmt.Sprintf("%-15s", f.label))
		if i == m.fcursor {
			cursor = stFrost.Render("› ")
			label = stValue.Render(fmt.Sprintf("%-15s", f.label))
		}
		if f.kind == cfExpander {
			caret := "▸"
			if m.advanced {
				caret = "▾"
			}
			fmt.Fprintf(&b, "%s%s %s\n", cursor, stFrost.Render(caret), label)
			if m.advanced {
				b.WriteString(stDim.Render(advExpander.help) + "\n")
			}
			continue
		}
		var val string
		if m.editing && i == m.fcursor && f.kind == cfText {
			val = m.input.View()
		} else {
			val = stFrost.Render(truncate(m.fieldValue(f.id), valW))
		}
		fmt.Fprintf(&b, "%s%s %s\n", cursor, label, val)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *appModel) guideBody(f cfField) string {
	var lines []string
	if ts := m.signinStatusLine(); ts != "" {
		lines = append(lines, ts)
	}
	if m.perr != "" {
		lines = append(lines, stRed.Render("! "+m.perr))
	}
	if m.storeNote != "" {
		lines = append(lines, stFrost.Render("• "+m.storeNote))
	}
	if m.ntfyNote != "" {
		st := stFrost
		if strings.HasPrefix(m.ntfyNote, "test notification failed") {
			st = stRed
		}
		lines = append(lines, st.Render("• "+m.ntfyNote))
	}
	lines = append(lines, stDim.Render(f.help))
	return strings.Join(lines, "\n")
}

// ntfyTestMsg is the result of the test notification fired when an ntfy topic is
// entered, so the user sees right away whether it reaches their phone.
type ntfyTestMsg struct{ err error }

// startNtfyTest fires a one-off test notification to the entered topic. A blank
// topic just clears any prior note (notifications are off).
func (m *appModel) startNtfyTest() tea.Cmd {
	target := resolveNtfyURL(m.cfg.ntfy)
	if target == "" {
		m.ntfyNote = ""
		return nil
	}
	m.ntfyNote = "sending a test notification…"
	return ntfyTestCmd(target)
}

func ntfyTestCmd(target string) tea.Cmd {
	return func() tea.Msg {
		err := sendNtfy(context.Background(), target,
			"discord-delete",
			"Test notification.",
			"default", "")
		return ntfyTestMsg{err: err}
	}
}

type visRow struct {
	guild int // index into m.guilds
	chIdx int // -1 for the guild header, else index into m.raws
}

// enterChannels opens the tree selector on the given scope, resetting the
// cursor and clearing any leftover search from a prior visit.
func (m *appModel) enterChannels(scope chanScope) {
	m.chanScope = scope
	m.ccursor = 0
	m.search.SetValue("")
	m.search.Blur()
	m.screen = scChannels
}

// channelRows flattens the active scope's guild tree into the currently-visible
// lines, honoring collapse state and the search query.
func (m *appModel) channelRows() []visRow {
	guilds, raws := m.scopeGuilds(), m.scopeRaws()
	q := strings.ToLower(strings.TrimSpace(m.search.Value()))
	var rows []visRow
	for gi := range guilds {
		g := guilds[gi]
		guildMatch := q != "" && strings.Contains(strings.ToLower(g.name), q)
		var matched []int
		for _, ci := range g.chans {
			if q == "" || guildMatch || strings.Contains(strings.ToLower(raws[ci].Label), q) {
				matched = append(matched, ci)
			}
		}
		if q != "" && len(matched) == 0 {
			continue
		}
		rows = append(rows, visRow{guild: gi, chIdx: -1})
		// Show channels when expanded, or always while searching.
		if g.open || q != "" {
			for _, ci := range matched {
				rows = append(rows, visRow{guild: gi, chIdx: ci})
			}
		}
	}
	return rows
}

// clampChanCursor pulls the tree cursor back onto a real row for a row set of n.
// The search box reshapes the set without going through the tree key handler, so
// an unclamped cursor indexes out of range and blanks the viewport.
func (m *appModel) clampChanCursor(n int) {
	if m.ccursor >= n {
		m.ccursor = n - 1
	}
	if m.ccursor < 0 {
		m.ccursor = 0
	}
}

// guildState reports the aggregate check state of a guild: all / none / some.
func (m *appModel) guildState(gi int) (all, none bool) {
	guilds, raws, sel := m.scopeGuilds(), m.scopeRaws(), m.scopeSelected()
	all, none = true, true
	for _, ci := range guilds[gi].chans {
		if sel[raws[ci].ChannelID] {
			none = false
		} else {
			all = false
		}
	}
	return
}

func (m *appModel) updateChannels(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Search input has focus: route typing to it, but keep esc/enter for control.
	if m.search.Focused() {
		key, ok := msg.(tea.KeyMsg)
		if ok {
			switch key.String() {
			case "esc":
				m.search.SetValue("")
				m.search.Blur()
				m.ccursor = 0
				return m, nil
			case "enter":
				m.search.Blur()
				m.ccursor = 0
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		m.clampChanCursor(len(m.channelRows()))
		return m, cmd
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	rows := m.channelRows()
	if len(rows) == 0 {
		if s := key.String(); s == "esc" || s == "b" || s == "q" {
			m.recompute()
			m.screen = scConfigure
		}
		return m, nil
	}
	m.clampChanCursor(len(rows))
	cur := rows[m.ccursor]
	guilds, raws, sel := m.scopeGuilds(), m.scopeRaws(), m.scopeSelected()

	switch key.String() {
	case "esc", "b", "q":
		m.recompute()
		m.screen = scConfigure
		return m, nil
	case "/":
		m.search.Focus()
		return m, nil
	case "up", "k":
		if m.ccursor > 0 {
			m.ccursor--
		}
	case "down", "j":
		if m.ccursor < len(rows)-1 {
			m.ccursor++
		}
	case "right", "l":
		if cur.chIdx == -1 {
			guilds[cur.guild].open = true
		}
	case "left", "h":
		if cur.chIdx == -1 {
			guilds[cur.guild].open = false
		}
	case "enter":
		if cur.chIdx == -1 {
			guilds[cur.guild].open = !guilds[cur.guild].open
		}
	case " ":
		if cur.chIdx == -1 {
			m.toggleGuild(cur.guild)
		} else {
			id := raws[cur.chIdx].ChannelID
			sel[id] = !sel[id]
		}
	case "a":
		m.setAllSelected(true)
	case "n":
		m.setAllSelected(false)
	}
	return m, nil
}

func (m *appModel) toggleGuild(gi int) {
	all, _ := m.guildState(gi)
	target := !all
	guilds, raws, sel := m.scopeGuilds(), m.scopeRaws(), m.scopeSelected()
	for _, ci := range guilds[gi].chans {
		sel[raws[ci].ChannelID] = target
	}
}

func (m *appModel) setAllSelected(v bool) {
	raws, sel := m.scopeRaws(), m.scopeSelected()
	for _, rc := range raws {
		sel[rc.ChannelID] = v
	}
}

func (m *appModel) viewChannels() string {
	var b strings.Builder
	sel, tot := m.scopeSelectionCounts()
	title := "channel selection"
	if m.chanScope == scopeReactions {
		title = "reaction channel selection"
	}
	b.WriteString(appHeader(m.width, title, badge(commafy(sel)+" / "+commafy(tot), nord8)) + "\n\n")
	if m.search.Focused() || m.search.Value() != "" {
		b.WriteString("  " + m.search.View() + "\n")
	}
	b.WriteString(panel("Servers & DMs", m.treeBody(m.width-4), m.width, nord9) + "\n")

	if m.search.Focused() {
		b.WriteString(wrapText(stKeyHelp.Render("type to filter · enter apply · esc clear"), m.width, 2) + "\n")
	} else {
		b.WriteString(wrapText(stKeyHelp.Render("↑/↓ move · space toggle · →/← or enter expand · a all · n none · / search · esc back"), m.width, 2) + "\n")
	}
	return b.String()
}

func (m *appModel) treeBody(innerW int) string {
	rows := m.channelRows()
	if len(rows) == 0 {
		return stDim.Render("no channels match the search")
	}
	guilds, raws, sel, noun := m.scopeGuilds(), m.scopeRaws(), m.scopeSelected(), m.scopeNoun()
	// Viewport: keeps the cursor within a visible window. The clamped copy makes
	// a window past the last row impossible whatever state ccursor is in.
	const window = 14
	cur := clampInt(m.ccursor, 0, len(rows)-1)
	start := 0
	if cur >= window {
		start = cur - window + 1
	}
	end := start + window
	if end > len(rows) {
		end = len(rows)
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		r := rows[i]
		cursor := "  "
		if i == cur {
			cursor = stFrost.Render("› ")
		}
		if r.chIdx == -1 {
			g := guilds[r.guild]
			all, none := m.guildState(r.guild)
			box := stYellow.Render("[~]")
			switch {
			case all:
				box = stGreen.Render("[x]")
			case none:
				box = stDim.Render("[ ]")
			}
			caret := "▸"
			if g.open || m.search.Value() != "" {
				caret = "▾"
			}
			nSel := 0
			for _, ci := range g.chans {
				if sel[raws[ci].ChannelID] {
					nSel++
				}
			}
			fmt.Fprintf(&b, "%s%s %s %s %s\n", cursor, stFrost.Render(caret), box,
				stValue.Render(truncate(g.name, clampInt(innerW-26, 12, 50))),
				stDim.Render(fmt.Sprintf("(%d/%d · %s %s)", nSel, len(g.chans), commafy(g.msgSum), noun)))
		} else {
			rc := raws[r.chIdx]
			box := stDim.Render("[ ]")
			if sel[rc.ChannelID] {
				box = stGreen.Render("[x]")
			}
			label := truncate(rc.Label, clampInt(innerW-18, 16, 60))
			fmt.Fprintf(&b, "     %s %s %s\n", box, label,
				stDim.Render(commafy(rc.items())))
		}
	}
	if len(rows) > window {
		b.WriteString(stDim.Render(fmt.Sprintf("… showing %d-%d of %d", start+1, end, len(rows))))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *appModel) updateTypes(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "esc", "b", "q":
		m.recompute()
		m.screen = scConfigure
		return m, nil
	case "up", "k":
		if m.tcursor > 0 {
			m.tcursor--
		}
	case "down", "j":
		if m.tcursor < len(typeOptions)-1 {
			m.tcursor++
		}
	case " ", "enter":
		id := typeOptions[m.tcursor].id
		if m.cfg.typeSel == nil {
			m.cfg.typeSel = map[string]bool{}
		}
		if m.cfg.typeSel[id] {
			delete(m.cfg.typeSel, id)
		} else {
			m.cfg.typeSel[id] = true
		}
		m.recompute()
	case "c":
		m.cfg.typeSel = map[string]bool{}
		m.recompute()
	}
	return m, nil
}

func (m *appModel) viewTypes() string {
	var b strings.Builder
	b.WriteString(appHeader(m.width, "message type", modeBadge(m.cfg.execute)) + "\n\n")
	title := fmt.Sprintf("Message type  ·  %s msgs match", commafy(m.total))
	b.WriteString(panel(title, m.typesBody(m.width-4), m.width, nord9) + "\n")
	b.WriteString(wrapText(stDim.Render(typeOptions[m.tcursor].desc), m.width, 2) + "\n")
	b.WriteString(wrapText(stKeyHelp.Render("↑/↓ move · space toggle · c clear (any) · esc back"), m.width, 2) + "\n")
	return b.String()
}

func (m *appModel) typesBody(innerW int) string {
	labelW := clampInt(innerW-14, 10, 24)
	var b strings.Builder
	for i, o := range typeOptions {
		cursor := "  "
		if i == m.tcursor {
			cursor = stFrost.Render("› ")
		}
		box := stDim.Render("[ ]")
		if m.cfg.typeSel[o.id] {
			box = stGreen.Render("[x]")
		}
		label := truncate(o.label, labelW)
		fmt.Fprintf(&b, "%s%s %s %s\n", cursor, box,
			stValue.Render(fmt.Sprintf("%-*s", labelW, label)),
			stDim.Render(commafy(m.typeCounts[o.id])+" msgs"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// sortGuilds orders servers alphabetically with the DM group pinned last.
func sortGuilds(g []guildGroup) {
	sort.SliceStable(g, func(i, j int) bool {
		if g[i].isDM != g[j].isDM {
			return !g[i].isDM
		}
		return strings.ToLower(g[i].name) < strings.ToLower(g[j].name)
	})
}
