package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

const reactionFixture = `{"event_type":"add_reaction","channel_id":"111","message_id":"940919514370015242","emoji_name":"🥒","guild_id":"g1","channel_type":"0","city":"SecretCity","ip":"1.2.3.4","isp":"Some ISP"}
{"event_type":"add_reaction","channel_id":"111","message_id":"940919514370015242","emoji_name":"🥒","guild_id":"g1","channel_type":"0"}
{"event_type":"add_reaction","channel_id":"111","message_id":"222","emoji_name":"pepe","emoji_id":"123456789","guild_id":"g1","channel_type":"0"}
{"event_type":"add_reaction","channel_id":"333","message_id":"444","emoji_name":"👍","channel_type":"1"}
{"event_type":"send_message","channel_id":"111","message_id":"555"}
{"event_type":"add_reaction","channel_id":"","message_id":"666","emoji_name":"x"}
{"event_type":"add_reaction","channel_id":"777","message_id":"888"}
`

func TestLoadReactions(t *testing.T) {
	fsys := fstest.MapFS{
		"Activity/reporting/events-2025-00000-of-00001.json": {Data: []byte(reactionFixture)},
	}
	got, err := loadReactions(fsys)
	if err != nil {
		t.Fatal(err)
	}
	// 3 unique: the duplicate 🥒 collapses; send_message, the empty-channel row,
	// and the no-emoji row (which would hit the engine's message-delete
	// sentinel) all drop.
	if len(got) != 3 {
		t.Fatalf("want 3 reactions, got %d: %+v", len(got), got)
	}
	by := map[string]Reaction{}
	for _, r := range got {
		by[r.MessageID] = r
	}
	if c := by["222"]; c.EmojiID != "123456789" || c.EmojiName != "pepe" || c.IsDM {
		t.Errorf("custom emoji reaction wrong: %+v", c)
	}
	if dm := by["444"]; !dm.IsDM || dm.GuildID != "" {
		t.Errorf("DM reaction should be IsDM with no guild: %+v", dm)
	}
	if u := by["940919514370015242"]; !u.HasSnow || u.Snowflake == 0 || u.GuildID != "g1" {
		t.Errorf("unicode reaction should have a parsed snowflake and guild: %+v", u)
	}
}

func TestEncodeReactionEmoji(t *testing.T) {
	cases := []struct {
		r    Reaction
		want string
	}{
		{Reaction{EmojiName: "🥒"}, "%F0%9F%A5%92"},
		{Reaction{EmojiName: "👍"}, "%F0%9F%91%8D"},
		{Reaction{EmojiName: "pepe", EmojiID: "123456789"}, "pepe%3A123456789"},
		{Reaction{EmojiName: "", EmojiID: "999"}, "e%3A999"},
	}
	for _, c := range cases {
		if got := encodeReactionEmoji(c.r); got != c.want {
			t.Errorf("encodeReactionEmoji(%+v) = %q, want %q", c.r, got, c.want)
		}
	}
}

func TestLoadGuildNames(t *testing.T) {
	fsys := fstest.MapFS{
		"Servers/index.json":        {Data: []byte(`{"g1":"Gophers","g2":"buildapc"}`)},
		"Servers/g3/guild.json":     {Data: []byte(`{"id":"g3","name":"Java Community"}`)},
		"Servers/g3/audit-log.json": {Data: []byte(`[]`)},
		"Messages/c1/messages.json": {Data: []byte(`[]`)},
	}
	names := loadGuildNames(fsys)
	if names["g1"] != "Gophers" || names["g2"] != "buildapc" || names["g3"] != "Java Community" {
		t.Fatalf("guild names wrong: %+v", names)
	}
}

func TestReadPackageCapabilities(t *testing.T) {
	writeMsg := func(dir string) {
		p := filepath.Join(dir, "Messages", "c123")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(p, "messages.json"), `[{"ID":"111","Contents":"hi"}]`)
	}
	writeReact := func(dir string) {
		p := filepath.Join(dir, "Activity", "reporting")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(p, "events-2025-00000-of-00001.json"), reactionFixture)
	}

	t.Run("messages only", func(t *testing.T) {
		d := t.TempDir()
		writeMsg(d)
		p, err := ReadPackage(d)
		if err != nil {
			t.Fatal(err)
		}
		if !p.Caps.HasMessages || p.Caps.HasReactions {
			t.Fatalf("caps = %+v", p.Caps)
		}
	})
	t.Run("reactions only", func(t *testing.T) {
		d := t.TempDir()
		writeReact(d)
		p, err := ReadPackage(d)
		if err != nil {
			t.Fatal(err)
		}
		if p.Caps.HasMessages || !p.Caps.HasReactions {
			t.Fatalf("caps = %+v", p.Caps)
		}
		if len(p.Reactions) != 3 {
			t.Fatalf("want 3 reactions, got %d", len(p.Reactions))
		}
	})
	t.Run("both", func(t *testing.T) {
		d := t.TempDir()
		writeMsg(d)
		writeReact(d)
		p, err := ReadPackage(d)
		if err != nil {
			t.Fatal(err)
		}
		if !p.Caps.HasMessages || !p.Caps.HasReactions {
			t.Fatalf("caps = %+v", p.Caps)
		}
	})
	t.Run("neither errors", func(t *testing.T) {
		if _, err := ReadPackage(t.TempDir()); err == nil {
			t.Fatal("empty package should error")
		}
	})
}

func TestApplyReactionFilter(t *testing.T) {
	reactions := []Reaction{
		{ChannelID: "a", GuildID: "g1", MessageID: "300", Snowflake: 300, HasSnow: true, EmojiName: "🥒"},
		{ChannelID: "a", GuildID: "g1", MessageID: "100", Snowflake: 100, HasSnow: true, EmojiName: "pepe", EmojiID: "9"},
		{ChannelID: "b", GuildID: "g2", MessageID: "200", Snowflake: 200, HasSnow: true, EmojiName: "👍"},
		{ChannelID: "d", IsDM: true, MessageID: "400", Snowflake: 400, HasSnow: true, EmojiName: "❤"},
	}
	names := map[string]string{"g1": "Gophers"}

	// No filter: 4 reactions across 3 channels, biggest channel first.
	jobs, total := ApplyReactionFilter(reactions, Filter{Order: "oldest"}, names)
	if total != 4 || len(jobs) != 3 {
		t.Fatalf("want 4 across 3 channels, got total=%d jobs=%d", total, len(jobs))
	}
	if jobs[0].ChannelID != "a" || jobs[0].count() != 2 {
		t.Errorf("channel a (2 items) should sort first: %+v", jobs[0])
	}
	// oldest-first order inside channel a: message 100 before 300.
	if jobs[0].Items[0].MessageID != "100" {
		t.Errorf("oldest-first should put 100 before 300: %+v", jobs[0].Items)
	}
	// custom emoji encoded as name:id.
	if jobs[0].Items[0].Emoji != "pepe%3A9" {
		t.Errorf("custom emoji encoding wrong: %q", jobs[0].Items[0].Emoji)
	}
	// label uses the guild name from Servers/.
	if jobs[0].Label != "Gophers / a" {
		t.Errorf("label = %q, want Gophers / a", jobs[0].Label)
	}

	// Channel filter to just b.
	_, tb := ApplyReactionFilter(reactions, Filter{Channels: map[string]bool{"b": true}}, names)
	if tb != 1 {
		t.Errorf("channel filter to b: want 1, got %d", tb)
	}
	// Date bound: only reactions on messages after snowflake 250.
	_, ta := ApplyReactionFilter(reactions, Filter{AfterID: 250}, names)
	if ta != 2 { // 300 and 400
		t.Errorf("after 250: want 2, got %d", ta)
	}
	// Resume set skips an already-done reaction key.
	done := map[string]bool{reactionKey(reactions[2]): true}
	_, td := ApplyReactionFilter(reactions, Filter{Done: done}, names)
	if td != 3 {
		t.Errorf("with one done: want 3, got %d", td)
	}
}

// TestReactionDeleteEndpoint drives the engine over a reaction job and asserts it
// hits the .../reactions/{emoji}/@me route and records the reaction resume key.
func TestReactionDeleteEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.RequestURI // raw target as sent (percent-encoded)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	apiBaseOverride = srv.URL

	var recorded []string
	stats := NewStats(1, 1)
	eng := NewEngine(EngineConfig{
		Workers: 1, DryRun: false, GlobalMinInterval: time.Millisecond,
		OnDeleted: func(k string) { recorded = append(recorded, k) },
	}, stats)
	job := ChannelJob{ChannelID: "111", Label: "x", Items: []deleteItem{
		{MessageID: "222", Emoji: "%F0%9F%A5%92", Key: "111|222|u:🥒"},
	}}
	eng.Run(context.Background(), []ChannelJob{job})

	if gotPath != "/channels/111/messages/222/reactions/%F0%9F%A5%92/@me" {
		t.Fatalf("reaction path wrong: %q", gotPath)
	}
	if s := stats.Snapshot(); s.Deleted != 1 {
		t.Fatalf("want 1 deleted, got %d", s.Deleted)
	}
	if len(recorded) != 1 || recorded[0] != "111|222|u:🥒" {
		t.Fatalf("resume key wrong: %v", recorded)
	}
}

// A reaction that reaches the filter with no emoji must never become a
// deleteItem: an empty Emoji is the engine's message-delete sentinel.
func TestReactionFilterDropsEmptyEmoji(t *testing.T) {
	rs := []Reaction{
		{ChannelID: "1", MessageID: "10"},
		{ChannelID: "1", MessageID: "11", EmojiName: "👍"},
	}
	jobs, total := ApplyReactionFilter(rs, Filter{}, nil)
	if total != 1 {
		t.Fatalf("want 1 item, got %d", total)
	}
	for _, j := range jobs {
		for _, it := range j.Items {
			if it.Emoji == "" {
				t.Fatal("empty Emoji leaked into a deleteItem")
			}
		}
	}
}
