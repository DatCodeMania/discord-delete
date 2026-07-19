package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// resolveNtfyURL turns a user-supplied value into a POST target. A full http(s)
// URL is used verbatim (self-hosted ntfy or a specific server); a bare word is
// treated as a topic on the public ntfy.sh.
func resolveNtfyURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	return "https://ntfy.sh/" + strings.TrimPrefix(s, "/")
}

// controlTarget derives the remote-control topic from the status target by
// appending "-ctl". Notifications sent to the status topic carry action buttons
// that POST a short command (pause/resume/stop) here; the running program
// subscribes to it and acts on them. Returns "" when notifications are off.
//
// Whoever can see your ntfy topic can post these commands. Pausing and stopping
// fail safe (they only ever slow or halt deletion, never start more), so this is
// a deliberate, low-risk trade for phone control. Use a self-hosted or
// authenticated ntfy server, or a hard-to-guess topic name, if that matters.
func controlTarget(statusTarget string) string {
	s := strings.TrimRight(statusTarget, "/")
	if s == "" {
		return ""
	}
	return s + "-ctl"
}

// ntfyAction is an http action button: tapping it POSTs `body` to `url`.
type ntfyAction struct {
	label string
	url   string
	body  string
}

// ntfyMessage is one notification: a title and body, plus optional priority,
// tags, and action buttons.
type ntfyMessage struct {
	title    string
	body     string
	priority string
	tags     string
	actions  []ntfyAction
}

// encodeActions renders action buttons into ntfy's `Actions` header form. Every
// button is an http POST of a single command word (no commas, spaces, or
// quotes), so the plain comma-separated form is unambiguous.
func encodeActions(as []ntfyAction) string {
	parts := make([]string, 0, len(as))
	for _, a := range as {
		parts = append(parts, fmt.Sprintf("http, %s, %s, method=POST, body=%s", a.label, a.url, a.body))
	}
	return strings.Join(parts, "; ")
}

// postNtfy sends one notification. Best-effort: the caller logs the error but
// never fails the run over it. The body is kept to non-sensitive counts/status:
// ntfy topics are public unless the user runs their own server, so no message
// content or channel names are ever sent.
func postNtfy(ctx context.Context, target string, m ntfyMessage) error {
	if target == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(m.body))
	if err != nil {
		return err
	}
	if m.title != "" {
		req.Header.Set("Title", m.title)
	}
	if m.priority != "" {
		req.Header.Set("Priority", m.priority)
	}
	if m.tags != "" {
		req.Header.Set("Tags", m.tags)
	}
	if len(m.actions) > 0 {
		req.Header.Set("Actions", encodeActions(m.actions))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned %s", resp.Status)
	}
	return nil
}

// sendNtfy posts a plain notification with no action buttons.
func sendNtfy(ctx context.Context, target, title, message, priority, tags string) error {
	return postNtfy(ctx, target, ntfyMessage{title: title, body: message, priority: priority, tags: tags})
}

// runningNtfy builds a live progress notification from a snapshot. `paused`
// switches the title, tags, and buttons (Pause vs Resume). ctlTarget enables the
// buttons; when it's "", the notification carries progress only, no controls.
func runningNtfy(pkg string, snap Snapshot, paused bool, ctlTarget string) ntfyMessage {
	pct := 0.0
	if snap.Total > 0 {
		pct = float64(snap.Processed) / float64(snap.Total) * 100
	}

	// No tags: ntfy renders tag shortcodes as emoji, and these carry none.
	state, tags, priority := "running", "", "low"
	if paused {
		state, priority = "paused", "default"
	}

	var b strings.Builder
	if pkg != "" {
		b.WriteString(pkg + "\n")
	}
	fmt.Fprintf(&b, "%s of %s deleted (%.1f%%)", commafy(int(snap.Deleted)), commafy(int(snap.Total)), pct)
	if snap.Skipped > 0 {
		fmt.Fprintf(&b, " · %s skipped", commafy(int(snap.Skipped)))
	}
	if snap.Failed > 0 {
		fmt.Fprintf(&b, " · %s failed", commafy(int(snap.Failed)))
	}
	fmt.Fprintf(&b, "\neta %s · %.2f msg/s", etaStr(snap), snap.Rate)
	if n := len(snap.Errors); n > 0 {
		fmt.Fprintf(&b, "\nlast error: %s", snap.Errors[n-1])
	}

	m := ntfyMessage{
		title:    fmt.Sprintf("discord-delete %s · %.0f%%", state, pct),
		body:     b.String(),
		priority: priority,
		tags:     tags,
	}
	if ctlTarget != "" {
		if paused {
			m.actions = []ntfyAction{
				{label: "Resume", url: ctlTarget, body: "resume"},
				{label: "Stop", url: ctlTarget, body: "stop"},
			}
		} else {
			m.actions = []ntfyAction{
				{label: "Pause", url: ctlTarget, body: "pause"},
				{label: "Stop", url: ctlTarget, body: "stop"},
			}
		}
	}
	return m
}
