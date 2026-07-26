package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tokenState is a token validation state. A token is only ever checked, never
// gated: the tool stores whatever the user typed regardless, and the check
// just gives early feedback so a bad or expired token is caught before a run
// starts instead of 400 deletes in.
type tokenState int

const (
	tsNone     tokenState = iota // no token, or not checked yet
	tsChecking                   // a /users/@me probe is in flight
	tsValid                      // 200, token works, m.tokenUser is set
	tsInvalid                    // 401, bad/expired token
	tsError                      // couldn't check (network/other); token untouched
)

// tokenCheckMsg is delivered when a /users/@me probe finishes. token is the
// exact value that was checked, so a stale result for an old token is ignored.
type tokenCheckMsg struct {
	token  string
	userID string
	user   string
	handle string
	state  tokenState
	err    string
}

type tokenIdentity struct {
	id     string
	name   string
	handle string
	state  tokenState
	err    string
}

// fetchTokenIdentity makes exactly one /users/@me request, the same
// account-scoped call a real client makes on load, and returns who the token
// belongs to. The token is only ever sent to Discord over HTTPS; it is never
// logged or written anywhere.
func fetchTokenIdentity(ctx context.Context, token string) tokenIdentity {
	token = normalizeToken(token)
	if token == "" {
		return tokenIdentity{state: tsNone}
	}
	if hasNonTokenChar(token) {
		return tokenIdentity{state: tsInvalid, err: "This token has characters that aren't part of a Discord token, so the paste was mangled. Re-copy it, or use Browser sign-in."}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL()+"/users/@me", nil)
	if err != nil {
		return tokenIdentity{state: tsError, err: err.Error()}
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return tokenIdentity{state: tsError, err: "network error, couldn't reach Discord"}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()

	switch resp.StatusCode {
	case 200:
		var u struct {
			ID            string `json:"id"`
			Username      string `json:"username"`
			Discriminator string `json:"discriminator"`
			GlobalName    string `json:"global_name"`
		}
		_ = json.Unmarshal(body, &u)
		return tokenIdentity{
			id:     u.ID,
			name:   friendlyUser(u.Username, u.Discriminator, u.GlobalName),
			handle: uniqueHandle(u.Username, u.Discriminator),
			state:  tsValid,
		}
	case 401:
		return tokenIdentity{state: tsInvalid, err: "Discord rejected this token (401). Usually: copied with extra characters, or it has expired. Try Browser sign-in."}
	case 429:
		return tokenIdentity{state: tsError, err: "rate-limited while checking; token may still be fine"}
	default:
		return tokenIdentity{state: tsError, err: "unexpected response while checking (HTTP " + strconv.Itoa(resp.StatusCode) + ")"}
	}
}

// fetchGuildMembership returns the set of guild IDs the token's account is
// currently a member of (GET /users/@me/guilds). It lets the end-of-run report
// tell "you left this server" from "still a member" for undeletable messages.
// Best-effort: any error returns nil, so the report simply omits those labels.
func fetchGuildMembership(ctx context.Context, token string) map[string]bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL()+"/users/@me/guilds", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var guilds []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &guilds) != nil {
		return nil
	}
	out := make(map[string]bool, len(guilds))
	for _, g := range guilds {
		if g.ID != "" {
			out[g.ID] = true
		}
	}
	return out
}

// checkTokenCmd probes the token asynchronously for the TUI.
func checkTokenCmd(token string) tea.Cmd {
	token = strings.TrimSpace(token)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		id := fetchTokenIdentity(ctx, token)
		return tokenCheckMsg{token: token, userID: id.id, user: id.name, handle: id.handle, state: id.state, err: id.err}
	}
}

// friendlyUser renders a human label from the fields of a Discord user object,
// handling both the legacy name#1234 and the newer unique-username schemes.
func friendlyUser(username, discriminator, global string) string {
	if global != "" {
		return global
	}
	if discriminator != "" && discriminator != "0" {
		return username + "#" + discriminator
	}
	if username == "" {
		return "your account"
	}
	return username
}

// normalizeToken trims whitespace and one pair of wrapping quotes. A Discord
// token never starts or ends with a quote, so stripping them is safe and undoes
// a common devtools/localStorage copy-paste mistake.
func normalizeToken(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if f, l := s[0], s[len(s)-1]; (f == '"' && l == '"') || (f == '\'' && l == '\'') {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	return s
}

func hasNonTokenChar(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_', r == '+', r == '/', r == '=':
		default:
			return true
		}
	}
	return false
}

// uniqueHandle is the account's unique @username (or legacy username#discriminator),
// as opposed to friendlyUser's non-unique display name. Empty if no username.
func uniqueHandle(username, discriminator string) string {
	username = strings.TrimSpace(username)
	if username == "" {
		return ""
	}
	if discriminator != "" && discriminator != "0" {
		return username + "#" + discriminator
	}
	return username
}

// startTokenCheck sets the checking state for the current token and returns the
// probe command (or clears state and returns nil for an empty token).
func (m *appModel) startTokenCheck() tea.Cmd {
	tok := strings.TrimSpace(m.cfg.token)
	m.browserErr = "" // a manual token supersedes a stale browser sign-in error
	if tok == "" {
		m.tokenState, m.tokenUser, m.tokenErr, m.tokenID, m.tokenHandle = tsNone, "", "", "", ""
		return nil
	}
	m.tokenState, m.tokenUser, m.tokenErr, m.tokenID, m.tokenHandle = tsChecking, "", "", "", ""
	return checkTokenCmd(tok)
}

// applyTokenCheck folds a finished probe into the model, ignoring stale results
// for a token that has since changed.
func (m *appModel) applyTokenCheck(msg tokenCheckMsg) {
	if strings.TrimSpace(m.cfg.token) != strings.TrimSpace(msg.token) {
		return
	}
	m.tokenState, m.tokenUser, m.tokenErr, m.tokenID, m.tokenHandle = msg.state, msg.user, msg.err, msg.userID, msg.handle
	if m.tokenState == tsValid {
		m.maybeSaveToken()
	}
}

// executeGuard returns the reason a real (execute) run must not proceed, or ""
// if the token, account, and resume-log checks pass. It is the single gate for
// every path into a live run: toggleExecute before flipping into execute, startRun before
// opening the confirm screen, and updateConfirm again on "y". That last re-check
// matters because the token probe is async: a probe that lands while the confirm
// screen is open (turning the account invalid or mismatched) would otherwise be
// bypassed, spending a run's requests on the wrong account.
func (m *appModel) executeGuard() string {
	if strings.TrimSpace(m.cfg.token) == "" {
		return "Execute needs a token. Set it in Configure, or pass --token / DISCORD_TOKEN."
	}
	if m.tokenState == tsInvalid {
		return "That token is invalid or expired. Fix it in Configure before executing."
	}
	if m.ownerMismatch() {
		return m.mismatchNote()
	}
	// An execute run must be resumable: without a working log every deletion
	// would be re-attempted by the next run, so refuse rather than run blind.
	for _, lg := range []struct {
		on   bool
		path string
	}{{m.cfg.delMessages, m.progPath}, {m.cfg.delReactions, m.reactProgPath}} {
		if !lg.on || lg.path == "" {
			continue
		}
		if err := probeProgressLog(lg.path); err != nil {
			return "Cannot open the resume log (" + err.Error() + "). Deletions would not be recorded, and the next run would repeat all of them. Point DISCORD_DELETE_STATE_DIR at a writable directory."
		}
	}
	return ""
}

// ownerMismatch reports whether we KNOW the token's account differs from the
// package's owner. It only fires when both identities are known, so a package
// without account/user.json (or a token not yet checked) never blocks.
func (m *appModel) ownerMismatch() bool {
	return m.ownerID != "" && m.tokenID != "" && m.ownerID != m.tokenID
}

// accountLabel formats an account as "handle (id)", falling back to the display
// name, then the bare id, when the handle is missing.
func accountLabel(handle, name, id string) string {
	if h := cmp.Or(handle, name); h != "" {
		return h + " (" + id + ")"
	}
	return id
}

func (m *appModel) mismatchNote() string {
	exportedBy := accountLabel(m.ownerHandle, m.ownerName, m.ownerID)
	signedInAs := accountLabel(m.tokenHandle, m.tokenUser, m.tokenID)
	return "This package was exported by " + exportedBy + ". Your token is " + signedInAs +
		". A package can only be cleared by the account that made it. Sign in as " + exportedBy +
		", or request a fresh export from " + signedInAs + "."
}

// startBrowserSignin kicks off the Chrome-launch capture flow, marking it active
// and returning the command that runs it. Cancellation is via m.browserCancel.
func (m *appModel) startBrowserSignin() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.browserCancel = cancel
	m.browserActive = true
	m.browserErr = ""
	return func() tea.Msg {
		tok, err := captureTokenFromBrowser(ctx)
		return browserSigninMsg{token: tok, err: err}
	}
}

// applyBrowserSignin folds a finished browser flow into the model. On success it
// returns the token-validation command so the captured token is verified like
// any other. On failure it records a friendly reason.
func (m *appModel) applyBrowserSignin(msg browserSigninMsg) tea.Cmd {
	m.browserActive = false
	m.browserCancel = nil
	if msg.err != nil {
		switch {
		case errors.Is(msg.err, context.Canceled):
			m.browserErr = "browser sign-in cancelled"
		case errors.Is(msg.err, errNoChrome):
			m.browserErr = "no Chrome/Chromium/Edge/Brave found; install one, set DISCORD_DELETE_CHROME if installed"
		default:
			m.browserErr = msg.err.Error()
		}
		return nil
	}
	m.browserErr = ""
	m.cfg.token = normalizeToken(msg.token)
	return m.startTokenCheck()
}

// signinStatusLine is the token/sign-in status shown on the home and configure
// screens: the live browser flow takes precedence, otherwise the token check.
func (m *appModel) signinStatusLine() string {
	if m.browserActive {
		return stFrost.Render("● Opening a browser. Log into Discord in the window that opened.  (esc cancels)")
	}
	if m.browserErr != "" {
		return stYellow.Render("⚠ " + m.browserErr)
	}
	return m.tokenStatusLine()
}

// tokenStatusLine is a short, colored one-liner describing the token check,
// suitable for the home and configure screens. Empty when there's nothing to say.
func (m *appModel) tokenStatusLine() string {
	switch m.tokenState {
	case tsChecking:
		return stFrost.Render("● checking token…")
	case tsValid:
		if m.ownerMismatch() {
			return stRed.Render("✗ " + m.mismatchNote())
		}
		return stGreen.Render("✓ signed in as " + accountLabel(m.tokenHandle, m.tokenUser, m.tokenID))
	case tsInvalid:
		return stRed.Render("✗ " + cmp.Or(m.tokenErr, "invalid token"))
	case tsError:
		return stYellow.Render("⚠ " + cmp.Or(m.tokenErr, "couldn't verify token") + " (you can still try)")
	}
	return ""
}
