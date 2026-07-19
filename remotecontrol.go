package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
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
	Event   string `json:"event"`
	Message string `json:"message"`
}

// subscribeControl streams commands from the control topic until ctx is done,
// calling onCmd for each recognized one. It reconnects with backoff so a dropped
// connection (or an ntfy restart) doesn't silently end remote control.
// Best-effort: every network error is swallowed. ntfy's default JSON stream
// sends only messages posted after we connect, so a stale command is never
// replayed on reconnect.
func subscribeControl(ctx context.Context, control string, onCmd func(controlCmd)) {
	if control == "" {
		return
	}
	streamURL := strings.TrimRight(control, "/") + "/json"
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		streamControlOnce(ctx, streamURL, onCmd)
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

// streamControlOnce holds one streaming connection open, dispatching every
// message-event command until the stream ends or ctx is cancelled.
func streamControlOnce(ctx context.Context, streamURL string, onCmd func(controlCmd)) {
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
		if c := parseControlCmd(msg.Message); c != "" {
			onCmd(c)
		}
	}
}
