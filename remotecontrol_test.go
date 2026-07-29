package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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

func newTestResume() *controlResume {
	return &controlResume{seen: map[string]bool{}}
}

// The resume marker rides in the query string: after ?auth=..., before #frag.
func TestWithSince(t *testing.T) {
	cases := map[string]string{
		"https://ntfy.sh/x-ctl/json":             "https://ntfy.sh/x-ctl/json?since=42",
		"https://ntfy.sh/x-ctl/json?auth=tk_abc": "https://ntfy.sh/x-ctl/json?auth=tk_abc&since=42",
		"https://ntfy.sh/x-ctl/json#frag":        "https://ntfy.sh/x-ctl/json?since=42#frag",
		"https://ntfy.sh/x-ctl/json?auth=t#frag": "https://ntfy.sh/x-ctl/json?auth=t&since=42#frag",
	}
	for in, want := range cases {
		if got := withSince(in, 42); got != want {
			t.Errorf("withSince(%q) = %q, want %q", in, got, want)
		}
	}
}

// ntfy's time filter is inclusive, so the boundary second is re-sent on every
// reconnect. Each command must still be dispatched exactly once.
func TestControlResumeDedupesBoundarySecond(t *testing.T) {
	r := newTestResume()
	a := ntfyStreamMsg{ID: "a1", Time: 100, Event: "message", Message: "pause"}
	b := ntfyStreamMsg{ID: "b2", Time: 100, Event: "message", Message: "stop"}

	if !r.accept(a) || !r.accept(b) {
		t.Fatal("first delivery of both messages should be accepted")
	}
	if r.from != 100 {
		t.Fatalf("marker = %d, want 100", r.from)
	}
	if r.accept(a) || r.accept(b) {
		t.Fatal("replayed boundary messages should be dropped")
	}

	// A later message advances the marker and retires the old dedupe set.
	c := ntfyStreamMsg{ID: "c3", Time: 101, Event: "message", Message: "resume"}
	if !r.accept(c) {
		t.Fatal("newer message should be accepted")
	}
	if r.from != 101 {
		t.Fatalf("marker = %d, want 101", r.from)
	}
	if len(r.seen) != 1 {
		t.Fatalf("seen should hold only the current second, got %d entries", len(r.seen))
	}
}

// An id-less message can't be deduplicated, so it is dispatched rather than
// dropped: swallowing a stop is the worse failure.
func TestControlResumeAcceptsIDLessMessage(t *testing.T) {
	r := newTestResume()
	m := ntfyStreamMsg{Time: 5, Event: "message", Message: "stop"}
	if !r.accept(m) {
		t.Fatal("id-less message should be accepted")
	}
	if !r.accept(m) {
		t.Fatal("a second id-less message should be accepted, not deduplicated away")
	}
}

// A command posted while the stream is down must survive the reconnect, and the
// boundary message ntfy re-sends with it must not fire twice.
func TestSubscribeControlResumesAfterDrop(t *testing.T) {
	// Commands are posted after the listener starts, so they sit ahead of the
	// marker subscribeControl seeds from the clock.
	t0 := time.Now().Unix() + 5
	pause := `{"id":"m1","time":` + strconv.FormatInt(t0, 10) + `,"event":"message","message":"pause"}`
	stop := `{"id":"m2","time":` + strconv.FormatInt(t0+1, 10) + `,"event":"message","message":"stop"}`

	var mu sync.Mutex
	var urls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		urls = append(urls, r.URL.String())
		first := len(urls) == 1
		mu.Unlock()
		if first {
			io.WriteString(w, pause+"\n") // then the connection drops
			return
		}
		io.WriteString(w, pause+"\n") // inclusive boundary replay
		io.WriteString(w, stop+"\n")  // sent while we were disconnected
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got []controlCmd
	done := make(chan struct{})
	go func() {
		defer close(done)
		subscribeControl(ctx, srv.URL, func(c controlCmd) {
			mu.Lock()
			got = append(got, c)
			mu.Unlock()
			if c == cmdStop {
				cancel()
			}
		})
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("subscribeControl never returned")
	}

	mu.Lock()
	defer mu.Unlock()
	want := []controlCmd{cmdPause, cmdStop}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v (pause must not fire twice)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cmd %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	if len(urls) < 2 {
		t.Fatalf("expected a reconnect, got %d request(s)", len(urls))
	}
	if strings.Contains(urls[0], "since=") {
		t.Errorf("first connect must not replay history, got %q", urls[0])
	}
	if want := "since=" + strconv.FormatInt(t0, 10); !strings.Contains(urls[1], want) {
		t.Errorf("reconnect = %q, want it to carry %q", urls[1], want)
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
	streamControlOnce(context.Background(), srv.URL, newTestResume(), func(c controlCmd) {
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
