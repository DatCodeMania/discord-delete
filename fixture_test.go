package main

import (
	"encoding/json"
	"testing"
)

// The testdata/packages/* fixtures mirror the real current Discord export format
// (numeric discriminator, string channel "type", capitalized message field names).

func findChan(raws []RawChannel, id string) *RawChannel {
	for i := range raws {
		if raws[i].ChannelID == id {
			return &raws[i]
		}
	}
	return nil
}

func TestFixtureModernPackageOwner(t *testing.T) {
	owner, ok := LoadPackageOwner("testdata/packages/modern")
	if !ok {
		t.Fatal("expected to read owner from the modern fixture")
	}
	if owner.ID != "100000000000000001" || owner.Handle != "testowner" {
		t.Fatalf("owner: id=%q handle=%q", owner.ID, owner.Handle)
	}
}

func TestFixtureModernGuildAttribution(t *testing.T) {
	raws, err := LoadRawPackage("testdata/packages/modern")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(raws) != 3 {
		t.Fatalf("channels: want 3, got %d", len(raws))
	}

	// Rich guild channel: guild embedded in channel.json; capital ID/Contents parse.
	if c := findChan(raws, "100000000000000010"); c == nil {
		t.Fatal("missing rich guild channel")
	} else if c.IsDM || c.GuildID != "200000000000000001" || c.GuildName != "Test Server" || c.Label != "#general (Test Server)" {
		t.Fatalf("rich guild channel: %+v", *c)
	} else if len(c.Messages) != 2 {
		t.Fatalf("rich guild channel messages: want 2, got %d", len(c.Messages))
	}

	// DM.
	if c := findChan(raws, "100000000000000011"); c == nil {
		t.Fatal("missing dm channel")
	} else if !c.IsDM || c.GuildID != "" {
		t.Fatalf("dm channel: %+v", *c)
	}

	// Left server: only the Messages index attributes it, by name (no stable id).
	if c := findChan(raws, "100000000000000012"); c == nil {
		t.Fatal("missing left-server channel")
	} else if c.IsDM || c.GuildID != "name:Old Server" || c.GuildName != "Old Server" {
		t.Fatalf("left-server channel: %+v", *c)
	}
}

func TestFixturePartialUnknownServer(t *testing.T) {
	raws, err := LoadRawPackage("testdata/packages/modern-partial")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c := findChan(raws, "100000000000000020"); c == nil {
		t.Fatal("missing guild channel")
	} else if c.IsDM || c.GuildID != "unknown-guild" || c.GuildName != "Unknown server" {
		t.Fatalf("unattributed guild channel should bucket as Unknown server: %+v", *c)
	}
	if c := findChan(raws, "100000000000000021"); c == nil {
		t.Fatal("missing dm channel")
	} else if !c.IsDM {
		t.Fatalf("dm channel misclassified: %+v", *c)
	}
}

func TestChannelTypeUnmarshal(t *testing.T) {
	cases := []struct {
		in     string
		want   channelType
		wantDM bool
	}{
		{`"GUILD_TEXT"`, "GUILD_TEXT", false},
		{`"DM"`, "DM", true},
		{`"GROUP_DM"`, "GROUP_DM", true},
		{`"PUBLIC_THREAD"`, "PUBLIC_THREAD", false},
		{`0`, "0", false},
		{`1`, "1", true},
		{`3`, "3", true},
		{`null`, "", false},
	}
	for _, c := range cases {
		var got channelType
		if err := json.Unmarshal([]byte(c.in), &got); err != nil {
			t.Errorf("unmarshal %s: %v", c.in, err)
			continue
		}
		if got != c.want || got.isDM() != c.wantDM {
			t.Errorf("type %s -> %q dm=%v, want %q dm=%v", c.in, got, got.isDM(), c.want, c.wantDM)
		}
	}
}

func TestAttributeFromLabel(t *testing.T) {
	idx := pkgIndexes{nameToID: map[string]string{
		"Test Server": "200000000000000001",
		"My Server":   "300000000000000001",
	}}
	cases := []struct{ label, cn, gid, gn string }{
		{"general in Test Server", "general", "200000000000000001", "Test Server"},
		{"team in prod in My Server", "team in prod", "300000000000000001", "My Server"}, // known name wins over an earlier " in "
		{"chat in Old Server", "chat", "name:Old Server", "Old Server"},                  // unknown server -> synthetic key
		{"nolabel", "", "", ""},
	}
	for _, c := range cases {
		cn, gid, gn := attributeFromLabel(c.label, idx)
		if cn != c.cn || gid != c.gid || gn != c.gn {
			t.Errorf("attributeFromLabel(%q) = (%q,%q,%q), want (%q,%q,%q)", c.label, cn, gid, gn, c.cn, c.gid, c.gn)
		}
	}
}
