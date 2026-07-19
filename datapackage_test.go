package main

import "testing"

// countTypes must tally every type option in one pass exactly as a per-option
// full scan would.
func TestCountTypes(t *testing.T) {
	raws := []RawChannel{
		{ChannelID: "1", Messages: []Message{
			newMessage("100", "plain text"),
			newMessage("101", "a link https://example.com"),
			newMessageFull("102", "", "photo.png"),
			newMessageFull("103", "clip", "video.mp4"),
		}},
		{ChannelID: "2", Messages: []Message{newMessage("104", "more text")}},
	}
	got := countTypes(raws)
	for _, o := range typeOptions {
		mask := typeIDMask(o.id)
		want := 0
		for _, rc := range raws {
			for _, m := range rc.Messages {
				if m.Kind&mask != 0 {
					want++
				}
			}
		}
		if got[o.id] != want {
			t.Errorf("countTypes[%s] = %d, want %d", o.id, got[o.id], want)
		}
	}
	if got["text"] == 0 {
		t.Error("expected text messages to be counted")
	}
}

// items counts messages for real channels and the ItemCount tally for the
// synthetic reaction channels (which carry no Message data).
func TestRawChannelItems(t *testing.T) {
	real := RawChannel{Messages: []Message{newMessage("1", "a"), newMessage("2", "b")}}
	if real.items() != 2 {
		t.Fatalf("message channel: want 2, got %d", real.items())
	}
	synth := RawChannel{ItemCount: 42}
	if synth.items() != 42 {
		t.Fatalf("synthetic channel: want 42, got %d", synth.items())
	}
}
