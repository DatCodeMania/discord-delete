package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// controlCmd is a command received from the phone (or any subscriber) on the
// control topic. It only ever slows or halts the run, never widens it.
type controlCmd string

const (
	cmdPause  controlCmd = "pause"
	cmdResume controlCmd = "resume"
	cmdStop   controlCmd = "stop"
)

// parseControlCmd maps a raw ntfy message body to a known command, or "" if it
// isn't one. Case-insensitive, with a few friendly synonyms.
func parseControlCmd(body string) controlCmd {
	switch strings.ToLower(strings.TrimSpace(body)) {
	case "pause", "hold":
		return cmdPause
	case "resume", "unpause", "continue", "go":
		return cmdResume
	case "stop", "cancel", "abort":
		return cmdStop
	}
	return ""
}

// ntfyStreamMsg is the subset of ntfy's JSON stream we read.
type ntfyStreamMsg struct {
	ID      string `json:"id"`
	Time    int64  `json:"time"`
	Event   string `json:"event"`
	Message string `json:"message"`
}

// controlResume marks how far the control topic has been read, so a reconnect
// asks ntfy for the commands posted during the gap instead of losing them.
//
// The marker is a timestamp, never a message id. ntfy resolves an id it no
// longer has cached to "every cached message", so on a run outliving the
// server's cache window a reconnect would replay commands from an earlier run.
// A timestamp only ever reaches forward. Its filter is inclusive, so the
// boundary second arrives twice; seen keeps each command dispatched once.
type controlResume struct {
	from int64           // unix seconds
	seen map[string]bool // ids already dispatched at t == from
}

// accept advances the marker and reports whether msg still needs dispatching.
func (r *controlResume) accept(msg ntfyStreamMsg) bool {
	// Deliberately no "older than the marker, drop it" rule: ntfy orders a
	// response by time, so the marker can only outrun messages still to come if
	// the server's clock trails ours, and dropping a live command over clock skew
	// is worse than the duplicate it would prevent.
	if msg.Time > r.from {
		r.from = msg.Time
		clear(r.seen)
	}
	// An id-less message can't be deduplicated; dispatch it rather than risk
	// swallowing a stop.
	if msg.ID == "" {
		return true
	}
	if r.seen[msg.ID] {
		return false
	}
	r.seen[msg.ID] = true
	return true
}

// subscribeControl streams commands from the control topic until ctx is done,
// calling onCmd for each recognized one. It reconnects with backoff so a dropped
// connection (or an ntfy restart) doesn't silently end remote control, resuming
// where it left off so a command sent during the gap still lands.
// Best-effort: every network error is swallowed. The first connection asks for
// nothing older than itself and the marker starts at process start, so a command
// left in ntfy's cache by an earlier run is never replayed into this one.
func subscribeControl(ctx context.Context, control string, onCmd func(controlCmd)) {
	if control == "" {
		return
	}
	streamURL := controlStreamURL(control)
	resume := &controlResume{from: time.Now().Unix(), seen: map[string]bool{}}
	backoff := time.Second
	reconnect := false
	for ctx.Err() == nil {
		url := streamURL
		if reconnect {
			url = withSince(streamURL, resume.from)
		}
		reconnect = true
		start := time.Now()
		streamControlOnce(ctx, url, resume, onCmd)
		if ctx.Err() != nil {
			return
		}
		// A connection that stayed up a while is healthy; reset the backoff so a
		// single blip after hours doesn't inherit a long delay.
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// controlStreamURL builds the ntfy JSON stream endpoint for a control target:
// /json goes on the topic path, before any ?auth=... query string.
func controlStreamURL(control string) string {
	base, rest := splitTargetQuery(control)
	return strings.TrimRight(base, "/") + "/json" + rest
}

// withSince attaches the resume marker, keeping it inside the query string:
// after any existing ?auth=..., ahead of any #fragment.
func withSince(streamURL string, since int64) string {
	head, frag := streamURL, ""
	if i := strings.IndexByte(streamURL, '#'); i >= 0 {
		head, frag = streamURL[:i], streamURL[i:]
	}
	sep := "?"
	if strings.Contains(head, "?") {
		sep = "&"
	}
	return head + sep + "since=" + strconv.FormatInt(since, 10) + frag
}

// streamControlOnce holds one streaming connection open, dispatching every
// message-event command until the stream ends or ctx is cancelled.
func streamControlOnce(ctx context.Context, streamURL string, resume *controlResume, onCmd func(controlCmd)) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return
	}
	// No client timeout: this is a long-lived stream, ended by ctx, not a clock.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg ntfyStreamMsg
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}
		if msg.Event != "message" {
			continue
		}
		// Marked read before parsing, so an unrecognized body still advances the
		// marker and isn't refetched on every reconnect.
		if !resume.accept(msg) {
			continue
		}
		if c := parseControlCmd(msg.Message); c != "" {
			onCmd(c)
		}
	}
}
