package main

import "testing"

func TestAttachmentKind(t *testing.T) {
	cases := []struct {
		url  string
		want MsgKind
	}{
		{"https://cdn.discordapp.com/attachments/1/2/cat.png?ex=abc&hm=def", KindImage},
		{"https://cdn.discordapp.com/attachments/1/2/clip.MP4", KindVideo},
		{"https://cdn.discordapp.com/attachments/1/2/song.mp3", KindAudio},
		{"https://cdn.discordapp.com/attachments/1/2/voice-message.ogg?ex=1", KindVoice},
		{"https://cdn.discordapp.com/attachments/1/2/notes.pdf", KindFile},
		{"https://cdn.discordapp.com/attachments/1/2/loop.ogg", KindAudio}, // non-voice ogg
	}
	for _, c := range cases {
		if got := attachmentKind(c.url); got != c.want {
			t.Errorf("attachmentKind(%q) = %b, want %b", c.url, got, c.want)
		}
	}
}

func TestClassifyMessage(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		attachments string
		want        MsgKind
	}{
		{"text only", "hello world", "", KindText},
		{"text with link", "see https://example.com", "", KindText | KindLink},
		{"image only, no text", "", "https://cdn/att/pic.jpg", KindImage},
		{"image with caption", "look", "https://cdn/att/pic.jpg", KindImage}, // not text-only (has attachment)
		{"image + video", "", "https://cdn/att/a.png https://cdn/att/b.mp4", KindImage | KindVideo},
		{"voice note", "", "https://cdn/att/voice-message.ogg", KindVoice},
		{"empty", "", "", 0},
	}
	for _, c := range cases {
		if got := classifyMessage(c.content, c.attachments); got != c.want {
			t.Errorf("%s: classifyMessage = %b, want %b", c.name, got, c.want)
		}
	}
}

func TestTypeFilterMultiSelect(t *testing.T) {
	raws := []RawChannel{{ChannelID: "1", Label: "#c", Messages: []Message{
		newMessageFull("1000000000000000001", "hi", ""),
		newMessageFull("1000000000000000002", "", "https://cdn/att/a.png"),
		newMessageFull("1000000000000000003", "", "https://cdn/att/b.mp4"),
		newMessageFull("1000000000000000004", "", "https://cdn/att/voice-message.ogg"),
		newMessageFull("1000000000000000005", "look https://x.com", ""),
	}}}

	check := func(sel map[string]bool, want int) {
		t.Helper()
		_, total := ApplyFilter(raws, Filter{Types: typesMask(sel)})
		if total != want {
			t.Errorf("types %v: want %d, got %d", sel, want, total)
		}
	}
	check(nil, 5) // any
	check(map[string]bool{"image": true}, 1)
	check(map[string]bool{"image": true, "video": true}, 2) // image OR video
	check(map[string]bool{"media": true}, 3)                // any attachment (image+video+voice)
	check(map[string]bool{"voice": true}, 1)
	check(map[string]bool{"text": true}, 2) // matches text-only and text+link
	check(map[string]bool{"link": true}, 1)
	check(map[string]bool{"text": true, "video": true}, 3) // 2 text + 1 video
}

func TestParseTypes(t *testing.T) {
	if _, err := parseTypes("image, voice"); err != nil {
		t.Fatalf("valid types errored: %v", err)
	}
	if _, err := parseTypes("image,bogus"); err == nil {
		t.Fatalf("expected error for unknown type")
	}
	sel, err := parseTypes("")
	if err != nil || sel != nil {
		t.Fatalf("empty should be (nil,nil), got (%v,%v)", sel, err)
	}
}
