package main

import "testing"

func TestCompileContentFilter(t *testing.T) {
	cases := []struct {
		in      string
		wantSub string
		wantRe  bool
		wantErr bool
		matches []string
		nomatch []string
	}{
		{in: "", wantSub: ""},
		{in: "hello", wantSub: "hello"},
		{in: "/foo", wantSub: "/foo"}, // no closing slash -> substring
		{in: "/ab.c/", wantRe: true, matches: []string{"abXc", "ab.c"}, nomatch: []string{"abc"}},
		{in: `/^gg$/`, wantRe: true, matches: []string{"gg"}, nomatch: []string{"ggg", "a gg"}},
		{in: "/Hi/i", wantRe: true, matches: []string{"hi there", "HI"}, nomatch: []string{"bye"}},
		{in: "/Hi/", wantRe: true, matches: []string{"Hi"}, nomatch: []string{"hi"}}, // case-sensitive
		{in: "/[/", wantErr: true},
	}
	for _, c := range cases {
		sub, re, err := compileContentFilter(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected an error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.in, err)
			continue
		}
		if sub != c.wantSub {
			t.Errorf("%q: substring = %q, want %q", c.in, sub, c.wantSub)
		}
		if (re != nil) != c.wantRe {
			t.Errorf("%q: regex set = %v, want %v", c.in, re != nil, c.wantRe)
		}
		for _, s := range c.matches {
			if re != nil && !re.MatchString(s) {
				t.Errorf("%q: expected regex to match %q", c.in, s)
			}
		}
		for _, s := range c.nomatch {
			if re != nil && re.MatchString(s) {
				t.Errorf("%q: expected regex NOT to match %q", c.in, s)
			}
		}
	}
}

// keepMsg should route through the regex when one is compiled, else the substring.
func TestKeepMsgRegexVsSubstring(t *testing.T) {
	_, re, err := compileContentFilter(`/\bhttps?:\/\/\S+/`)
	if err != nil {
		t.Fatal(err)
	}
	fRe := Filter{ContentRe: re}
	if !keepMsg(newMessage("1", "see https://example.com"), fRe) {
		t.Fatal("regex should keep a message containing a URL")
	}
	if keepMsg(newMessage("2", "no link here"), fRe) {
		t.Fatal("regex should drop a message without a URL")
	}

	// Substring path stays case-insensitive.
	fSub := Filter{Content: "HELLO"}
	if !keepMsg(newMessage("3", "well hello there"), fSub) {
		t.Fatal("substring match should be case-insensitive")
	}
}
