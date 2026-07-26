package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  abc.def.ghi  ", "abc.def.ghi"},
		{`"abc.def.ghi"`, "abc.def.ghi"},
		{"'abc.def.ghi'", "abc.def.ghi"},
		{` "abc.def.ghi" `, "abc.def.ghi"},
		{`abc"def`, `abc"def`},             // unwrapped inner quote, untouched
		{`"abc.def.ghi'`, `"abc.def.ghi'`}, // mismatched quotes, left as-is
		{"", ""},
		{`""`, ""},
	}
	for _, c := range cases {
		if got := normalizeToken(c.in); got != c.want {
			t.Errorf("normalizeToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHasNonTokenChar(t *testing.T) {
	valid := []string{
		"aB3-x9_Q.Yz-2c.aB_cd-19XyZ", // base64url shape, incl. - and _
		"mfa.abcDEF012_-",            // legacy mfa shape
		"abc123._-+/=",               // full allowed superset
	}
	for _, v := range valid {
		if hasNonTokenChar(v) {
			t.Errorf("hasNonTokenChar(%q) = true, want false (real tokens must never be flagged)", v)
		}
	}
	mangled := []string{
		"abc def.ghi.jkl", // space (truncated/multi-line paste)
		"abc\tdef",        // tab
		"abc\ndef",        // newline
		"abc def",         // non-breaking space from a web copy
		"abcé.def",        // non-ascii
		`abc"def`,         // stray quote
	}
	for _, m := range mangled {
		if !hasNonTokenChar(m) {
			t.Errorf("hasNonTokenChar(%q) = false, want true (mangled paste)", m)
		}
	}
}

func TestFriendlyUser(t *testing.T) {
	cases := []struct{ user, disc, global, want string }{
		{"alice", "0", "Alice A.", "Alice A."}, // new scheme with a display name
		{"bob", "1234", "", "bob#1234"},        // legacy discriminator
		{"carol", "0", "", "carol"},            // new scheme, no display name
		{"", "0", "", "your account"},          // nothing usable
	}
	for _, c := range cases {
		if got := friendlyUser(c.user, c.disc, c.global); got != c.want {
			t.Errorf("friendlyUser(%q,%q,%q) = %q, want %q", c.user, c.disc, c.global, got, c.want)
		}
	}
}

func TestCheckTokenValid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "good-token" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/users/@me" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"username":"me","discriminator":"0","global_name":"Me Myself"}`))
	}))
	defer srv.Close()
	old := apiBaseOverride
	apiBaseOverride = srv.URL
	defer func() { apiBaseOverride = old }()

	msg := checkTokenCmd("good-token")().(tokenCheckMsg)
	if msg.state != tsValid {
		t.Fatalf("want tsValid, got %v (err %q)", msg.state, msg.err)
	}
	if msg.user != "Me Myself" {
		t.Fatalf("want user %q, got %q", "Me Myself", msg.user)
	}
}

func TestCheckTokenInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	old := apiBaseOverride
	apiBaseOverride = srv.URL
	defer func() { apiBaseOverride = old }()

	msg := checkTokenCmd("bad-token")().(tokenCheckMsg)
	if msg.state != tsInvalid {
		t.Fatalf("want tsInvalid, got %v", msg.state)
	}
}

func TestCheckTokenEmptyIsNone(t *testing.T) {
	msg := checkTokenCmd("   ")().(tokenCheckMsg)
	if msg.state != tsNone {
		t.Fatalf("empty token should be tsNone, got %v", msg.state)
	}
}

// A manually entered token must clear a stale browser sign-in error, or the
// home screen keeps showing "sign-in failed" over a now-valid token.
func TestStartTokenCheckClearsBrowserErr(t *testing.T) {
	m := demoModel()
	m.browserErr = "browser sign-in cancelled"
	m.cfg.token = "abc.def.ghi"
	m.startTokenCheck()
	if m.browserErr != "" {
		t.Fatalf("browserErr should clear on a manual token check, got %q", m.browserErr)
	}
}

// applyTokenCheck must ignore a stale probe whose token no longer matches the model's current token.
func TestApplyTokenCheckIgnoresStale(t *testing.T) {
	m := demoModel()
	m.cfg.token = "new-token"
	m.tokenState = tsChecking
	m.applyTokenCheck(tokenCheckMsg{token: "old-token", state: tsValid, user: "Ghost"})
	if m.tokenState != tsChecking || m.tokenUser != "" {
		t.Fatalf("stale result should be ignored, got state=%v user=%q", m.tokenState, m.tokenUser)
	}
	m.applyTokenCheck(tokenCheckMsg{token: "new-token", state: tsValid, user: "Real"})
	if m.tokenState != tsValid || m.tokenUser != "Real" {
		t.Fatalf("matching result should apply, got state=%v user=%q", m.tokenState, m.tokenUser)
	}
}
