package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveNtfyURL(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"  ":                     "",
		"mytopic":                "https://ntfy.sh/mytopic",
		"/mytopic":               "https://ntfy.sh/mytopic",
		"https://ntfy.sh/x":      "https://ntfy.sh/x",
		"http://localhost/topic": "http://localhost/topic",
	}
	for in, want := range cases {
		if got := resolveNtfyURL(in); got != want {
			t.Errorf("resolveNtfyURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSendNtfyPostsTitleAndBody(t *testing.T) {
	var gotTitle, gotBody, gotTags string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.Header.Get("Title")
		gotTags = r.Header.Get("Tags")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	err := sendNtfy(context.Background(), srv.URL, "the title", "the body", "high", "warning")
	if err != nil {
		t.Fatal(err)
	}
	if gotTitle != "the title" || gotBody != "the body" || gotTags != "warning" {
		t.Fatalf("got title=%q body=%q tags=%q", gotTitle, gotBody, gotTags)
	}
}

func TestSendNtfyEmptyTargetIsNoop(t *testing.T) {
	if err := sendNtfy(context.Background(), "", "t", "b", "", ""); err != nil {
		t.Fatalf("empty target should be a no-op, got %v", err)
	}
}

func TestControlTarget(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"https://ntfy.sh/x":       "https://ntfy.sh/x-ctl",
		"https://ntfy.sh/x/":      "https://ntfy.sh/x-ctl",
		"http://localhost/topic":  "http://localhost/topic-ctl",
		"https://ntfy.sh/my-runs": "https://ntfy.sh/my-runs-ctl",
	}
	for in, want := range cases {
		if got := controlTarget(in); got != want {
			t.Errorf("controlTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeActions(t *testing.T) {
	got := encodeActions([]ntfyAction{
		{label: "Pause", url: "https://ntfy.sh/x-ctl", body: "pause"},
		{label: "Stop", url: "https://ntfy.sh/x-ctl", body: "stop"},
	})
	want := "http, Pause, https://ntfy.sh/x-ctl, method=POST, body=pause; " +
		"http, Stop, https://ntfy.sh/x-ctl, method=POST, body=stop"
	if got != want {
		t.Errorf("encodeActions =\n  %q\nwant\n  %q", got, want)
	}
}

// TestRunningNtfy checks the progress body carries the percent, counts, and ETA,
// and that the action buttons flip between Pause and Resume with the paused flag.
func TestRunningNtfy(t *testing.T) {
	snap := Snapshot{Total: 1000, Deleted: 250, Skipped: 10, Failed: 2, Processed: 262, Rate: 3.5}
	ctl := "https://ntfy.sh/x-ctl"

	run := runningNtfy("mypkg.zip", snap, false, ctl)
	if !strings.Contains(run.body, "26.2%") || !strings.Contains(run.body, "250 of 1,000") {
		t.Errorf("running body missing progress: %q", run.body)
	}
	if !strings.Contains(run.body, "2 failed") || !strings.Contains(run.body, "eta ") {
		t.Errorf("running body missing failed/eta: %q", run.body)
	}
	if len(run.actions) != 2 || run.actions[0].body != "pause" {
		t.Errorf("running should offer a pause button, got %+v", run.actions)
	}

	paused := runningNtfy("mypkg.zip", snap, true, ctl)
	if len(paused.actions) != 2 || paused.actions[0].body != "resume" {
		t.Errorf("paused should offer a resume button, got %+v", paused.actions)
	}

	// With no control target, progress still renders but carries no buttons.
	noctl := runningNtfy("", snap, false, "")
	if len(noctl.actions) != 0 {
		t.Errorf("no control target should mean no buttons, got %+v", noctl.actions)
	}
}

func TestPostNtfyIncludesActionsHeader(t *testing.T) {
	var gotActions string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotActions = r.Header.Get("Actions")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	msg := ntfyMessage{
		title:   "t",
		body:    "b",
		actions: []ntfyAction{{label: "Pause", url: "https://x/ctl", body: "pause"}},
	}
	if err := postNtfy(context.Background(), srv.URL, msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotActions, "http, Pause,") || !strings.Contains(gotActions, "body=pause") {
		t.Errorf("Actions header not sent correctly: %q", gotActions)
	}
}
