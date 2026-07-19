package main

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// runReport is the end-of-run summary, written to disk and (optionally) pushed
// via ntfy. It holds only counts and status, never message content. A run has one
// result per operation it performed (messages, reactions, or both).
type runReport struct {
	Package   string
	Execute   bool
	StartedAt time.Time
	EndedAt   time.Time
	Results   []opResult
	Resumed   int // messages skipped up front as already-deleted
}

// opResult is one operation's outcome (a message phase or a reaction phase).
type opResult struct {
	Kind      string // "messages" or "reactions"
	Snap      Snapshot
	Collapsed bool
	Forbidden []forbiddenServer
}

func (o opResult) noun() string {
	if o.Kind == "reactions" {
		return "reaction"
	}
	return "message"
}

// verb is the past-tense action for counts: deleted/removed for a real run,
// matched for a dry run.
func (o opResult) verb(execute bool) string {
	if !execute {
		return "matched"
	}
	if o.Kind == "reactions" {
		return "removed"
	}
	return "deleted"
}

// membershipState labels whether the account still belongs to a server that has
// undeletable items. memberUnknown means we didn't (or couldn't) check.
type membershipState int

const (
	memberUnknown membershipState = iota
	memberYes
	memberNo
)

// forbiddenServer is a tally of items that came back 403. Server entries
// aggregate all a guild's channels; DMs (and channels not found in the package)
// are listed individually with their ChannelID.
type forbiddenServer struct {
	Server    string
	IsDM      bool
	ChannelID string // set for a single DM/channel entry; "" for an aggregated server
	Messages  int    // item count (messages or reactions)
	Channels  int
	Member    membershipState
	Reason    string // Discord's reason for the 403 (dominant one, if several)
}

func (r runReport) anyAborted() bool {
	for _, o := range r.Results {
		if o.Snap.Aborted {
			return true
		}
	}
	return false
}

func (r runReport) allCompleted() bool {
	for _, o := range r.Results {
		if !o.Snap.Completed {
			return false
		}
	}
	return len(r.Results) > 0
}

// status is a short "completed"/"aborted"/"stopped" word for titles. Only a run
// where every phase processed all its work is "completed"; a user stop is
// "stopped".
func (r runReport) status() string {
	switch {
	case r.anyAborted():
		return "aborted"
	case r.allCompleted():
		return "completed"
	default:
		return "stopped"
	}
}

func (r runReport) mode() string {
	if r.Execute {
		return "delete"
	}
	return "dry run"
}

func (r runReport) notifyTitle() string {
	return fmt.Sprintf("discord-delete %s %s", r.mode(), r.status())
}

func (r runReport) notifyBody() string {
	var b strings.Builder
	if r.Package != "" {
		b.WriteString(r.Package + "\n")
	}
	for i, o := range r.Results {
		if i > 0 {
			b.WriteString("\n")
		}
		prefix := ""
		if len(r.Results) > 1 {
			prefix = o.Kind + ": "
		}
		fmt.Fprintf(&b, "%s%s %s, %s skipped, %s failed of %s", prefix,
			commafy(int(o.Snap.Deleted)), o.verb(r.Execute),
			commafy(int(o.Snap.Skipped)), commafy(int(o.Snap.Failed)),
			commafy(int(o.Snap.Total)))
	}
	fmt.Fprintf(&b, " in %s", r.elapsed())
	switch {
	case r.anyAborted():
		b.WriteString("\nAborted early (the token looks invalid). Re-run after fixing it; done items are skipped.")
	case !r.allCompleted():
		b.WriteString("\nStopped before finishing. Re-run to resume where you left off.")
	}
	return b.String()
}

// notifyTags is intentionally empty: ntfy renders tag shortcodes as emoji, and
// the notifications carry no emoji. Priority still conveys severity.
func (r runReport) notifyTags() string {
	return ""
}

func (r runReport) notifyPriority() string {
	if r.anyAborted() {
		return "high"
	}
	return "default"
}

func (r runReport) elapsed() time.Duration {
	if !r.EndedAt.IsZero() && !r.StartedAt.IsZero() {
		return r.EndedAt.Sub(r.StartedAt).Round(time.Second)
	}
	if len(r.Results) > 0 {
		return r.Results[0].Snap.Elapsed.Round(time.Second)
	}
	return 0
}

// text renders the full human-readable report file.
func (r runReport) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "discord-delete run report\n=========================\n\n")
	fmt.Fprintf(&b, "package    %s\n", cmp.Or(r.Package, "(unknown)"))
	fmt.Fprintf(&b, "mode       %s\n", r.mode())
	fmt.Fprintf(&b, "status     %s\n", r.status())
	if !r.StartedAt.IsZero() {
		fmt.Fprintf(&b, "started    %s\n", r.StartedAt.Format(time.RFC3339))
	}
	if !r.EndedAt.IsZero() {
		fmt.Fprintf(&b, "ended      %s\n", r.EndedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "elapsed    %s\n", r.elapsed())

	for _, o := range r.Results {
		r.writeOpSection(&b, o)
	}
	return b.String()
}

// writeOpSection renders one phase's counts, notes, undeletable rollup, and
// recent errors.
func (r runReport) writeOpSection(b *strings.Builder, o opResult) {
	verb := o.verb(r.Execute)
	if !r.Execute {
		verb += " (dry run, nothing was deleted)"
	}
	// A header per section only when there is more than one.
	if len(r.Results) > 1 {
		fmt.Fprintf(b, "\n[%s]\n", o.Kind)
	} else {
		fmt.Fprintf(b, "\n")
	}
	fmt.Fprintf(b, "%-10s %s\n", verb, commafy(int(o.Snap.Deleted)))
	fmt.Fprintf(b, "%-10s %s\n", "skipped", commafy(int(o.Snap.Skipped)))
	fmt.Fprintf(b, "%-10s %s\n", "failed", commafy(int(o.Snap.Failed)))
	fmt.Fprintf(b, "%-10s %s\n", "total", commafy(int(o.Snap.Total)))

	if o.Kind == "messages" && r.Resumed > 0 {
		fmt.Fprintf(b, "\nresumed    %s message(s) were settled in a prior run (deleted, or undeletable\n           system messages) and skipped up front.\n", commafy(r.Resumed))
	}
	if o.Snap.Skipped > 0 {
		fmt.Fprintf(b, "\nSkipped %ss are either undeletable (system messages / lost access) or were\ndeferred under heavy rate-limiting. Re-run to retry deferred.\n", o.noun())
	}
	if o.Collapsed {
		fmt.Fprintf(b, "\nNote: collapsed to a single worker by an account-wide rate limit. Expected for\nold (>~2-week) messages, where extra workers don't help.\n")
	}
	if len(o.Forbidden) > 0 {
		fmt.Fprintf(b, "\nUndeletable %ss (403), grouped, with the reason Discord returned:\n", o.noun())
		for _, fs := range o.Forbidden {
			where := fmt.Sprintf("in %s channel(s)", commafy(fs.Channels))
			if fs.ChannelID != "" { // a single DM or unknown channel
				where = "channel " + fs.ChannelID
			}
			line := fmt.Sprintf("  %-26s %8s  %s", truncate(fs.Server, 26), commafy(fs.Messages), where)
			switch fs.Member {
			case memberNo:
				line += "  (you left this server)"
			case memberYes:
				line += "  (still a member)"
			}
			fmt.Fprintf(b, "%s\n", line)
			if fs.Reason != "" {
				fmt.Fprintf(b, "      reason: %s\n", fs.Reason)
			}
		}
		fmt.Fprintf(b, "\nReasons: system messages (calls, group renames, adds and removes) and items\n"+
			"that aren't yours can never be deleted. A permissions/access reason means you\n"+
			"lost access; rejoin the server or reopen the DM, then re-run. A re-run retries\n"+
			"what skipped and skips what's already deleted.\n")
	}
	if len(o.Snap.Errors) > 0 {
		fmt.Fprintf(b, "\nRecent errors (last %d):\n", len(o.Snap.Errors))
		for _, e := range o.Snap.Errors {
			fmt.Fprintf(b, "  - %s\n", e)
		}
	}
}

// chanMeta is the guild/DM info a channel needs for the undeletable rollup.
type chanMeta struct {
	GuildID   string
	GuildName string
	Label     string
	IsDM      bool
}

// metaFromRaws builds channel metadata from the message package.
func metaFromRaws(raws []RawChannel) map[string]chanMeta {
	m := make(map[string]chanMeta, len(raws))
	for _, rc := range raws {
		m[rc.ChannelID] = chanMeta{GuildID: rc.GuildID, GuildName: rc.GuildName, Label: rc.Label, IsDM: rc.IsDM}
	}
	return m
}

// metaFromReactions builds channel metadata from reaction channels, naming
// guilds from Servers/.
func metaFromReactions(reactions []Reaction, guildNames map[string]string) map[string]chanMeta {
	m := map[string]chanMeta{}
	for _, r := range reactions {
		if _, ok := m[r.ChannelID]; ok {
			continue
		}
		m[r.ChannelID] = chanMeta{GuildID: r.GuildID, GuildName: guildNames[r.GuildID], IsDM: r.IsDM}
	}
	return m
}

// forbiddenByServer rolls per-channel 403 tallies up to servers, naming each from
// the channel metadata and keeping Discord's dominant reason. members (guildID ->
// true, from GET /users/@me/guilds) is nil when membership wasn't checked, in
// which case the "you left this" labels are omitted. Sorted most-undeletable
// first.
func forbiddenByServer(forbidden map[string]ForbiddenStat, meta map[string]chanMeta, members map[string]bool) []forbiddenServer {
	if len(forbidden) == 0 {
		return nil
	}
	type agg struct {
		name    string
		gid     string
		chanID  string // set for DMs / unknown channels (listed individually)
		isDM    bool
		msgs    int
		chans   int
		reasons map[string]int // reason -> count, to pick the dominant one
	}
	groups := map[string]*agg{}
	for chID, stat := range forbidden {
		var key string
		g := &agg{reasons: map[string]int{}}
		switch md, ok := meta[chID]; {
		case !ok:
			key, g.name, g.chanID = "ch:"+chID, "Unknown channel", chID
		case md.IsDM:
			key, g.name, g.chanID, g.isDM = "dm:"+chID, cmp.Or(md.Label, "Direct message"), chID, true
		default:
			g.gid = md.GuildID
			key = "g:" + md.GuildID
			g.name = md.GuildName
			if g.name == "" {
				g.name = "Server " + md.GuildID
			}
		}
		if cur, ok := groups[key]; ok {
			g = cur
		} else {
			groups[key] = g
		}
		g.msgs += stat.Count
		g.chans++
		if stat.Reason != "" {
			g.reasons[stat.Reason] += stat.Count
		}
	}
	out := make([]forbiddenServer, 0, len(groups))
	for _, g := range groups {
		fs := forbiddenServer{Server: g.name, IsDM: g.isDM, ChannelID: g.chanID, Messages: g.msgs, Channels: g.chans, Reason: dominantReason(g.reasons)}
		if members != nil && !g.isDM && g.gid != "" {
			if members[g.gid] {
				fs.Member = memberYes
			} else {
				fs.Member = memberNo
			}
		}
		out = append(out, fs)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Messages != out[j].Messages {
			return out[i].Messages > out[j].Messages
		}
		return out[i].Server < out[j].Server
	})
	return out
}

// dominantReason returns the reason with the most items behind it, appending a
// note when the group has more than one distinct reason.
func dominantReason(reasons map[string]int) string {
	if len(reasons) == 0 {
		return ""
	}
	best, bestN := "", -1
	for r, n := range reasons {
		if n > bestN || (n == bestN && r < best) {
			best, bestN = r, n
		}
	}
	if len(reasons) > 1 {
		return best + " (and other reasons)"
	}
	return best
}

// forbiddenTotal sums undeletable (403) items across all channels.
func forbiddenTotal(snap Snapshot) int {
	n := 0
	for _, c := range snap.Forbidden {
		n += c.Count
	}
	return n
}

// destPath picks where to write: an explicit --report override wins; otherwise a
// timestamped file beside the resume log. Returns "" only if neither is known.
func (r runReport) destPath(override, progPath string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if progPath == "" {
		return ""
	}
	return reportPathFor(progPath, r.StartedAt)
}

// reportPathFor derives a timestamped report path next to the resume log.
func reportPathFor(progPath string, startedAt time.Time) string {
	base := strings.TrimSuffix(progPath, ".deleted.log")
	if base == "" {
		base = filepath.Join(progressDir(), "run")
	}
	stamp := "run"
	if !startedAt.IsZero() {
		stamp = startedAt.Format("20060102-150405")
	}
	return base + "-" + stamp + ".report.txt"
}

func writeRunReport(path string, r runReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(r.text()), 0o600)
}
