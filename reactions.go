package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Reaction is one of your reactions, read from the package's Activity/reporting
// telemetry (add_reaction events). The Messages export has none of this, so a
// Messages-only package yields no reactions.
type Reaction struct {
	ChannelID string
	GuildID   string // "" for DMs
	MessageID string
	Snowflake uint64 // parsed from MessageID, for date filtering by target message
	HasSnow   bool
	EmojiName string
	EmojiID   string // set for custom emoji, "" for unicode
	IsDM      bool
}

// reactionEvent is the subset of a reporting event we read. Every other field
// (which includes IP-derived location, ISP, device, locale) is deliberately
// ignored and never leaves this parser.
type reactionEvent struct {
	EventType   string `json:"event_type"`
	ChannelID   string `json:"channel_id"`
	MessageID   string `json:"message_id"`
	EmojiName   string `json:"emoji_name"`
	EmojiID     string `json:"emoji_id"`
	GuildID     string `json:"guild_id"`
	ChannelType string `json:"channel_type"`
}

// reactionKey uniquely identifies a reaction for dedup and the resume log. The
// emoji is keyed by id for custom (names collide) and by name for unicode.
func reactionKey(r Reaction) string {
	emoji := "u:" + r.EmojiName
	if r.EmojiID != "" {
		emoji = "c:" + r.EmojiID
	}
	return r.ChannelID + "|" + r.MessageID + "|" + emoji
}

// loadReactions streams the reporting event shards, keeps add_reaction events,
// and dedups them. The file can be ~1GB, so it is scanned line by line and only
// the handful of needed fields are decoded. A read/scan failure is an error
// rather than a silently shorter list.
func loadReactions(fsys fs.FS) ([]Reaction, error) {
	var files []string
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		lp := strings.ToLower(p)
		base := strings.ToLower(d.Name())
		if strings.Contains(lp, "activity/reporting/") &&
			strings.HasPrefix(base, "events") && strings.HasSuffix(base, ".json") {
			files = append(files, p)
		}
		return nil
	})

	needle := []byte("add_reaction")
	seen := map[string]bool{}
	var out []Reaction
	for _, f := range files {
		rc, err := fsys.Open(f)
		if err != nil {
			return nil, fmt.Errorf("open reaction events %q: %w", f, err)
		}
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if !bytes.Contains(line, needle) {
				continue
			}
			var e reactionEvent
			if json.Unmarshal(line, &e) != nil || e.EventType != "add_reaction" {
				continue
			}
			if e.ChannelID == "" || e.MessageID == "" {
				continue
			}
			// An empty Emoji is the engine's delete-the-whole-message sentinel,
			// so a reaction with no emoji must never become a deleteItem.
			if e.EmojiName == "" && e.EmojiID == "" {
				continue
			}
			r := Reaction{
				ChannelID: e.ChannelID,
				GuildID:   e.GuildID,
				MessageID: e.MessageID,
				EmojiName: e.EmojiName,
				EmojiID:   e.EmojiID,
				IsDM:      e.GuildID == "" || e.ChannelType == "1" || e.ChannelType == "3",
			}
			if n, perr := strconv.ParseUint(e.MessageID, 10, 64); perr == nil {
				r.Snowflake, r.HasSnow = n, true
			}
			key := reactionKey(r)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, r)
		}
		// A scan error means part of the file was never seen; error rather than
		// silently report a "complete" run over a truncated reaction list.
		serr := sc.Err()
		rc.Close()
		if serr != nil {
			return nil, fmt.Errorf("read reaction events %q: %w", f, serr)
		}
	}
	return out, nil
}

// loadGuildNames maps guild id to name from the Servers/ export, so the reaction
// channel picker can show real server names (reaction channels aren't in the
// Messages export, so this is the only in-package name source).
func loadGuildNames(fsys fs.FS) map[string]string {
	out := map[string]string{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		lp := strings.ToLower(p)
		base := strings.ToLower(d.Name())
		switch {
		case base == "index.json" && strings.HasSuffix(strings.ToLower(path.Dir(p)), "servers"):
			if data, e := fs.ReadFile(fsys, p); e == nil {
				var idx map[string]string
				if json.Unmarshal(data, &idx) == nil {
					for id, name := range idx {
						if name != "" {
							out[id] = name
						}
					}
				}
			}
		case base == "guild.json" && strings.Contains(lp, "servers/"):
			if data, e := fs.ReadFile(fsys, p); e == nil {
				var g struct{ ID, Name string }
				if json.Unmarshal(data, &g) == nil && g.ID != "" && g.Name != "" {
					out[g.ID] = g.Name
				}
			}
		}
		return nil
	})
	return out
}

// encodeReactionEmoji builds the {emoji} path segment for the reactions endpoint:
// unicode emoji are the percent-encoded UTF-8 bytes; custom emoji are name:id,
// also percent-encoded. A bad segment is what triggers 10014 Unknown Emoji.
func encodeReactionEmoji(r Reaction) string {
	raw := r.EmojiName
	if r.EmojiID != "" {
		name := r.EmojiName
		if name == "" {
			name = "e" // name is cosmetic; Discord resolves custom emoji by id
		}
		raw = name + ":" + r.EmojiID
	}
	return percentEncode(raw)
}

// percentEncode escapes every byte except RFC3986 unreserved, so the emoji bytes
// and the custom-emoji colon are all encoded.
func percentEncode(s string) string {
	const hexd = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hexd[c>>4])
			b.WriteByte(hexd[c&0xF])
		}
	}
	return b.String()
}

// PackageCapabilities is what a loaded package can act on.
type PackageCapabilities struct {
	HasMessages  bool
	HasReactions bool
}

// LoadedPackage is everything read from a package in one pass.
type LoadedPackage struct {
	Caps       PackageCapabilities
	Raws       []RawChannel
	Reactions  []Reaction
	GuildNames map[string]string // guild id -> name, from Servers/
	Owner      PackageOwner      // zero value if the package didn't record it
}

// reactionChannelLabel names a reaction's channel for the picker and report.
// Reaction channels aren't in the Messages export, so names come from Servers/
// (guild only) and the channel is shown by id.
func reactionChannelLabel(r Reaction, guildNames map[string]string) string {
	if r.IsDM || r.GuildID == "" {
		return "DM " + r.ChannelID
	}
	if name := guildNames[r.GuildID]; name != "" {
		return name + " / " + r.ChannelID
	}
	return "channel " + r.ChannelID + " (server " + r.GuildID + ")"
}

// ApplyReactionFilter turns reactions into per-channel jobs, honoring channel/
// guild selection, the shared date bounds (by target-message snowflake), the
// resume set (reaction keys), and per-channel order. The message-only Filter
// fields (Content, Types) are ignored.
func ApplyReactionFilter(reactions []Reaction, f Filter, guildNames map[string]string) ([]ChannelJob, int) {
	byCh := map[string]int{}
	var jobs []ChannelJob
	total := 0
	for _, r := range reactions {
		// nil = no channel filter; a non-nil empty set means NONE selected.
		if f.Channels != nil && !f.Channels[r.ChannelID] {
			continue
		}
		if f.Guilds != nil && !f.Guilds[r.GuildID] {
			continue
		}
		if f.AfterID != 0 || f.BeforeID != 0 {
			if !r.HasSnow {
				continue
			}
			if f.AfterID != 0 && r.Snowflake <= f.AfterID {
				continue
			}
			if f.BeforeID != 0 && r.Snowflake >= f.BeforeID {
				continue
			}
		}
		key := reactionKey(r)
		if f.Done[key] {
			continue
		}
		emoji := encodeReactionEmoji(r)
		if emoji == "" {
			continue // empty Emoji is the message-delete sentinel, never a reaction
		}
		idx, ok := byCh[r.ChannelID]
		if !ok {
			jobs = append(jobs, ChannelJob{
				ChannelID: r.ChannelID, GuildID: r.GuildID,
				Label: reactionChannelLabel(r, guildNames),
			})
			idx = len(jobs) - 1
			byCh[r.ChannelID] = idx
		}
		jobs[idx].Items = append(jobs[idx].Items, deleteItem{
			MessageID: r.MessageID, Emoji: emoji, Key: key,
		})
		total++
	}
	for i := range jobs {
		sortItems(jobs[i].Items, f.Order)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].count() > jobs[j].count() })
	return jobs, total
}

// sortItems orders reaction items by their target-message snowflake, decoding
// each id once instead of inside the comparator.
func sortItems(items []deleteItem, order string) {
	type kv struct {
		it deleteItem
		n  uint64
	}
	decorated := make([]kv, len(items))
	for i, it := range items {
		n, _ := strconv.ParseUint(it.MessageID, 10, 64)
		decorated[i] = kv{it, n}
	}
	newest := order == "newest"
	sort.SliceStable(decorated, func(i, j int) bool {
		if newest {
			return decorated[i].n > decorated[j].n
		}
		return decorated[i].n < decorated[j].n
	})
	for i, d := range decorated {
		items[i] = d.it
	}
}

// reactionRawChannels builds one synthetic RawChannel per reaction channel so
// the reaction channel picker can reuse the message channel-selector machinery.
// Messages is sized to the reaction count so the selector's per-guild totals work.
func reactionRawChannels(reactions []Reaction, guildNames map[string]string) []RawChannel {
	counts := map[string]int{}
	first := map[string]Reaction{}
	var order []string
	for _, r := range reactions {
		if _, ok := first[r.ChannelID]; !ok {
			first[r.ChannelID] = r
			order = append(order, r.ChannelID)
		}
		counts[r.ChannelID]++
	}
	out := make([]RawChannel, 0, len(order))
	for _, cid := range order {
		r := first[cid]
		out = append(out, RawChannel{
			ChannelID: cid,
			GuildID:   r.GuildID,
			GuildName: guildNames[r.GuildID],
			IsDM:      r.IsDM,
			Label:     reactionChannelLabel(r, guildNames),
			ItemCount: counts[cid], // no zero-value Message padding just for a count
		})
	}
	return out
}
