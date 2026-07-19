package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// pickerModel is the first-run screen, shown when the tool launches with no
// --package and a terminal is present. It explains the data package, lets the
// user point at the .zip or an already-extracted folder, and on success hands
// the parsed channels back to main so main doesn't reparse the zip.
type pickerModel struct {
	input  textinput.Model
	width  int
	height int

	hint    string
	hintOK  bool // hint is a good sign (green) rather than a nudge (dim)
	errMsg  string
	loading bool

	// Result, read by main after the program exits.
	chosen string
	pkg    *LoadedPackage
	quit   bool
}

type pkgLoadedMsg struct {
	path string
	pkg  *LoadedPackage
	err  error
}

func newPickerModel() *pickerModel {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = "path to package.zip/the folder where it's extracted"
	ti.Width = 60
	ti.Focus()
	staticCursorForDemo(&ti)
	return &pickerModel{input: ti, width: 90, height: 30}
}

func (m *pickerModel) Init() tea.Cmd { return textinput.Blink }

func (m *pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = clampInt(msg.Width-16, 20, 80)
		return m, nil
	case pkgLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.chosen, m.pkg = msg.path, msg.pkg
		return m, tea.Quit
	case tea.KeyMsg:
		if m.loading {
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		case "enter":
			p := cleanPickerPath(m.input.Value())
			if p == "" {
				m.errMsg = "type or paste a path first"
				return m, nil
			}
			m.errMsg = ""
			m.loading = true
			return m, loadPackageCmd(p)
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.hint, m.hintOK = pathHint(cleanPickerPath(m.input.Value()))
	return m, cmd
}

// loadPackageCmd reads the package off the main loop so the UI can show a
// "reading…" state while a large .zip is parsed.
func loadPackageCmd(p string) tea.Cmd {
	return func() tea.Msg {
		pkg, err := ReadPackage(p)
		return pkgLoadedMsg{path: p, pkg: pkg, err: err}
	}
}

func (m *pickerModel) View() string {
	var b strings.Builder
	b.WriteString(appHeader(m.width, "first run", badge("SETUP", nord8)) + "\n\n")
	b.WriteString(panel("Welcome to discord-delete", pickerWelcomeBody(), m.width, nord8) + "\n")
	b.WriteString(panel("Your data package", m.pickerSelectBody(), m.width, nord14) + "\n")

	switch {
	case m.loading:
		b.WriteString(wrapText(stKeyHelp.Render("reading the package…"), m.width, 2) + "\n")
	default:
		b.WriteString(wrapText(stKeyHelp.Render("enter open · ctrl+v paste · esc quit"), m.width, 2) + "\n")
	}
	return b.String()
}

func pickerWelcomeBody() string {
	return strings.Join([]string{
		stValue.Render("This tool bulk-deletes your Discord messages."),
		stDim.Render("Deletion runs off the data package. It holds every message and ID, so no requests are burned searching Discord."),
		"",
		stFrost.Render("Don't have the package yet? Request it from Discord:"),
		stDim.Render("  1. Open Discord, go to Settings, then Data & Privacy."),
		stDim.Render("  2. Click \"Request Data\" and select Messages (to delete messages) and Activity (to remove reactions). The more you select, the longer the package takes to generate."),
		stDim.Render("  3. Discord emails you a .zip when it's ready. This can take between a few hours to a few days."),
	}, "\n")
}

func (m *pickerModel) pickerSelectBody() string {
	lines := []string{
		stLabel.Render("Select your data package (.zip, or the unzipped folder):"),
		"  " + m.input.View(),
	}
	switch {
	case m.loading:
		lines = append(lines, stFrost.Render("  reading the package…"))
	case m.errMsg != "":
		lines = append(lines, stRed.Render("  ✗ "+m.errMsg))
	case m.hint != "" && m.hintOK:
		lines = append(lines, stGreen.Render("  ✓ ")+stFrost.Render(m.hint))
	case m.hint != "":
		lines = append(lines, stDim.Render("  · "+m.hint))
	default:
		lines = append(lines, stDim.Render("  paste the path or drag the file onto this window, then press Enter"))
	}
	return strings.Join(lines, "\n")
}

// pathHint gives quick feedback on the typed path without loading it. The bool
// reports whether it's worth pressing Enter.
func pathHint(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	info, err := os.Stat(p)
	if err != nil {
		return "nothing at that path yet", false
	}
	if info.IsDir() {
		return "folder found, press Enter to open it", true
	}
	if strings.HasSuffix(strings.ToLower(p), ".zip") {
		return "zip found, press Enter to open it", true
	}
	return "file found, press Enter to read it as a package", true
}

// cleanPickerPath normalizes a pasted or dragged-in path: it trims spaces, drops
// one layer of surrounding quotes, unescapes the "\ " that some terminals insert
// for spaces on drag-and-drop, and expands a leading ~ to the home directory.
func cleanPickerPath(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			s = s[1 : len(s)-1]
		}
	}
	s = strings.ReplaceAll(s, `\ `, " ")
	s = strings.TrimSpace(s)
	if s == "~" || strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			s = filepath.Join(home, s[1:])
		}
	}
	return s
}
