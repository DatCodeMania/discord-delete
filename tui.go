package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

func demoMode() bool { return os.Getenv("DEV_DISCORD_DELETE_DEMO") != "" }

func staticCursorForDemo(ti *textinput.Model) {
	if demoMode() {
		ti.Cursor.SetMode(cursor.CursorStatic)
	}
}

// Nord palette: https://www.nordtheme.com/
const (
	nord0  = "#2E3440" // polar night (bg)
	nord1  = "#3B4252"
	nord2  = "#434C5E"
	nord3  = "#4C566A" // muted
	nord4  = "#D8DEE9" // snow storm (fg)
	nord5  = "#E5E9F0"
	nord6  = "#ECEFF4" // brightest fg
	nord7  = "#8FBCBB" // frost teal
	nord8  = "#88C0D0" // frost cyan (primary accent)
	nord9  = "#81A1C1" // frost blue
	nord10 = "#5E81AC" // frost deep blue
	nord11 = "#BF616A" // aurora red (errors)
	nord12 = "#D08770" // aurora orange (warn/failed)
	nord13 = "#EBCB8B" // aurora yellow (skipped)
	nord14 = "#A3BE8C" // aurora green (success)
	nord15 = "#B48EAD" // aurora purple
)

var (
	stTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(nord8))
	stSub     = lipgloss.NewStyle().Foreground(lipgloss.Color(nord3))
	stLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color(nord9))
	stValue   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(nord6))
	stGreen   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(nord14))
	stYellow  = lipgloss.NewStyle().Foreground(lipgloss.Color(nord13))
	stOrange  = lipgloss.NewStyle().Foreground(lipgloss.Color(nord12))
	stRed     = lipgloss.NewStyle().Foreground(lipgloss.Color(nord11))
	stFrost   = lipgloss.NewStyle().Foreground(lipgloss.Color(nord7))
	stDim     = lipgloss.NewStyle().Foreground(lipgloss.Color(nord3))
	stKeyHelp = lipgloss.NewStyle().Foreground(lipgloss.Color(nord3)).Italic(true)
)

type tickMsg time.Time

func doTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// wrapText word-wraps s (which may contain ANSI styling) to the content width
// and indents each line by `indent` spaces. Used everywhere a long UI line
// might overflow a narrow terminal.
func wrapText(s string, width, indent int) string {
	w := width - indent
	if w < 12 {
		w = 12
	}
	wrapped := lipgloss.NewStyle().Width(w).Render(s)
	if indent <= 0 {
		return wrapped
	}
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(wrapped, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// osc8 wraps text in an OSC 8 hyperlink: clickable where the terminal supports
// it, and invisible everywhere else.
func osc8(target, text string) string {
	return "\x1b]8;;" + target + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// fileURL is the file:// form of path. Drive letters need the extra leading
// slash, and a UNC path's server is the URL's host rather than part of its path.
func fileURL(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	p := filepath.ToSlash(abs)
	if rest, ok := strings.CutPrefix(p, "//"); ok {
		host, tail, _ := strings.Cut(rest, "/")
		return (&url.URL{Scheme: "file", Host: host, Path: "/" + tail}).String()
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// linkPath makes already-rendered text open path when clicked.
func linkPath(path, text string) string { return osc8(fileURL(path), text) }

// linkWrapped links each line of already-wrapped text on its own, skipping the
// indent and the trailing padding wrapText adds. Terminals underline a hyperlink,
// so covering the padding draws a rule across the whole frame.
func linkWrapped(path, wrapped string) string {
	lines := strings.Split(wrapped, "\n")
	for i, ln := range lines {
		body := strings.TrimRight(ln, " ")
		trail := ln[len(body):]
		lead := body[:len(body)-len(strings.TrimLeft(body, " "))]
		if body = body[len(lead):]; body == "" {
			continue
		}
		lines[i] = lead + linkPath(path, body) + trail
	}
	return strings.Join(lines, "\n")
}

// plainPathLink is linkPath for --no-tui runs, minus the escapes when stdout is
// piped somewhere that would only have to strip them.
func plainPathLink(path string) string {
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return path
	}
	return linkPath(path, path)
}

// hitBox is a clickable region of the frame: row, inclusive column span, 0-based.
type hitBox struct{ y, x0, x1 int }

func (h hitBox) contains(x, y int) bool { return y == h.y && x >= h.x0 && x <= h.x1 }

const (
	btnOpenReport = "↗ open report"
	btnIndent     = 2
)

// reportButton renders the end-of-run button on row y and reports the cells it
// covers. A narrow frame breaks the label across two rows and the hit box then
// covers only the first one, where the click has to land anyway. The OSC 8 link
// is the fallback: terminals only follow it on ctrl/cmd+click.
func reportButton(path string, width, y int) (string, hitBox) {
	// Wrapped first, linked after: the escapes stay out of the wrap.
	line := linkWrapped(path, wrapText(stKeyHelp.Render(btnOpenReport), width, btnIndent))
	// Measuring the unstyled wrap keeps escapes out of the way; the TrimRight
	// drops the right padding wrapText adds.
	head, _, _ := strings.Cut(wrapText(btnOpenReport, width, btnIndent), "\n")
	return line, hitBox{y: y, x0: btnIndent, x1: lipgloss.Width(strings.TrimRight(head, " ")) - 1}
}

// twoColMin is the terminal width below which side-by-side panels stack.
const twoColMin = 78

// badge renders a small filled pill (dark text on a colored background).
func badge(text, bg string) string {
	return lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(nord0)).Background(lipgloss.Color(bg)).
		Padding(0, 1).Render(text)
}

// button renders a wider filled pill for a primary action.
func button(text, bg string) string {
	return lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(nord0)).Background(lipgloss.Color(bg)).
		Padding(0, 2).Render(text)
}

// appHeader renders the consistent top bar: the app title on the left and a
// right-aligned status badge (e.g. the DRY RUN / EXECUTE mode).
func appHeader(width int, sub, badge string) string {
	left := "  " + stTitle.Render("discord-delete") + "  " + stSub.Render("· "+sub)
	gap := width - lipgloss.Width(left) - lipgloss.Width(badge) - 2
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + badge + " "
}

// panel draws a rounded box with `title` in the top border and `body` inside,
// sized to exactly `width` columns. The body is width-clamped so nothing
// overflows the box on a small terminal.
func panel(title, body string, width int, accent string) string {
	if width < 10 {
		width = 10
	}
	innerW := width - 4 // "│ " … " │"
	if innerW < 4 {
		innerW = 4
	}
	bs := lipgloss.NewStyle().Foreground(lipgloss.Color(nord3))
	ts := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(accent))

	// Top border: ╭─ title ─────╮
	tvis := title
	fill := width - 5 - lipgloss.Width(tvis) // "╭─ " (3) + title + " " (1) + fill + "╮" (1)
	if fill < 0 {
		tvis = truncate(title, width-5)
		fill = width - 5 - lipgloss.Width(tvis)
	}
	if fill < 0 {
		fill = 0
	}
	var b strings.Builder
	b.WriteString(bs.Render("╭─ ") + ts.Render(tvis) + bs.Render(" "+strings.Repeat("─", fill)+"╮") + "\n")

	block := lipgloss.NewStyle().Width(innerW).Render(body)
	for _, ln := range strings.Split(block, "\n") {
		pad := innerW - lipgloss.Width(ln)
		if pad < 0 {
			pad = 0
		}
		b.WriteString(bs.Render("│ ") + ln + strings.Repeat(" ", pad) + bs.Render(" │") + "\n")
	}
	b.WriteString(bs.Render("╰" + strings.Repeat("─", width-2) + "╯"))
	return b.String()
}

// twoCol lays two prebuilt blocks side by side, or stacks them on a narrow
// terminal. Callers size each block to colWidth(width) beforehand.
func twoCol(width int, left, right string) string {
	if width < twoColMin {
		return left + "\n" + right
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
}

// colWidth is the per-column panel width for a responsive two-column row.
func colWidth(width int) int {
	if width < twoColMin {
		return width
	}
	return (width - 2) / 2
}

// kv renders a padded "label   value" row for a panel body; value may be styled.
func kv(label string, labelW int, val string) string {
	return stLabel.Render(fmt.Sprintf("%-*s", labelW, label)) + "  " + val
}

// commafy groups an integer with thousands separators (401845 -> "401,845").
func commafy(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// sparkline renders values as a compact bar chart using block glyphs, showing
// the most recent `width` samples (the running screen's rate history).
func sparkline(vals []float64, width int, color string) string {
	if width <= 0 || len(vals) == 0 {
		return ""
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	maxV := 0.0
	for _, v := range vals {
		if v > maxV {
			maxV = v
		}
	}
	if maxV <= 0 {
		maxV = 1
	}
	var sb strings.Builder
	// Left-pad with the lowest glyph so the chart is right-aligned as it fills.
	for i := 0; i < width-len(vals); i++ {
		sb.WriteRune(blocks[0])
	}
	for _, v := range vals {
		idx := clampInt(int(v/maxV*float64(len(blocks)-1)+0.5), 0, len(blocks)-1)
		sb.WriteRune(blocks[idx])
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(sb.String())
}

func miniBar(done, total, width int) string {
	if total <= 0 {
		total = 1
	}
	filled := int(float64(done) / float64(total) * float64(width))
	filled = clampInt(filled, 0, width)
	full := lipgloss.NewStyle().Foreground(lipgloss.Color(nord8)).Render(strings.Repeat("█", filled))
	empty := lipgloss.NewStyle().Foreground(lipgloss.Color(nord2)).Render(strings.Repeat("░", width-filled))
	return full + empty
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	mi := d / time.Minute
	d -= mi * time.Minute
	s := d / time.Second
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, mi, s)
	}
	if mi > 0 {
		return fmt.Sprintf("%dm%02ds", mi, s)
	}
	return fmt.Sprintf("%ds", s)
}

// etaStr renders the ETA for the tally and the ntfy body. A pause is a choice,
// so it must not read as a stall.
func etaStr(s Snapshot, paused bool) string {
	if s.Finished || s.Processed >= s.Total {
		return "done"
	}
	if paused {
		return "paused"
	}
	if s.ETA <= 0 {
		if s.Deleted > 0 {
			return "stalled"
		}
		return "…"
	}
	return fmtDur(s.ETA)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
