package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseControlCmd(t *testing.T) {
	cases := map[string]controlCmd{
		"pause":     cmdPause,
		"  PAUSE  ": cmdPause,
		"hold":      cmdPause,
		"resume":    cmdResume,
		"unpause":   cmdResume,
		"continue":  cmdResume,
		"stop":      cmdStop,
		"cancel":    cmdStop,
		"abort":     cmdStop,
		"":          "",
		"delete":    "",
		"whatever":  "",
	}
	for in, want := range cases {
		if got := parseControlCmd(in); got != want {
			t.Errorf("parseControlCmd(%q) = %q, want %q", in, got, want)
		}
	}
}

// The /json stream segment belongs on the topic path, ahead of any auth query.
func TestControlStreamURL(t *testing.T) {
	cases := map[string]string{
		"https://ntfy.sh/x-ctl":              "https://ntfy.sh/x-ctl/json",
		"https://ntfy.sh/x-ctl/":             "https://ntfy.sh/x-ctl/json",
		"https://ntfy.sh/x-ctl?auth=tk_abc":  "https://ntfy.sh/x-ctl/json?auth=tk_abc",
		"https://ntfy.sh/x-ctl/?auth=tk_abc": "https://ntfy.sh/x-ctl/json?auth=tk_abc",
	}
	for in, want := range cases {
		if got := controlStreamURL(in); got != want {
			t.Errorf("controlStreamURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStreamControlOnce confirms only message-event commands are dispatched,
// in order, ignoring open/keepalive events and unknown message bodies.
func TestStreamControlOnce(t *testing.T) {
	lines := []string{
		`{"event":"open","topic":"t"}`,
		`{"event":"message","topic":"t","message":"pause"}`,
		`{"event":"keepalive","topic":"t"}`,
		`{"event":"message","topic":"t","message":"hello"}`,
		`{"event":"message","topic":"t","message":"resume"}`,
		`{"event":"message","topic":"t","message":"stop"}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, l := range lines {
			io.WriteString(w, l+"\n")
		}
	}))
	defer srv.Close()

	var got []controlCmd
	streamControlOnce(context.Background(), srv.URL, func(c controlCmd) {
		got = append(got, c)
	})

	want := []controlCmd{cmdPause, cmdResume, cmdStop}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cmd %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}
