package main

import (
	"archive/zip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Message is one message loaded from the package, with its snowflake decoded
// up front so filtering by date/order is cheap when done repeatedly (the TUI
// re-filters live as the user tweaks the configuration).
type Message struct {
	ID        string
	Snowflake uint64
	HasSnow   bool
	Content   string
	Kind      MsgKind // media/text/link classification, computed once at load
}

// MsgKind is a bitmask of what a message contains, derived entirely from the
// package (attachment file extensions and the message text), no API requests.
// A single message can carry several bits (e.g. an image and a link).
type MsgKind uint8

const (
	KindText  MsgKind = 1 << iota // text, and no attachments
	KindImage                     // png/jpg/gif/webp/…
	KindVideo                     // mp4/mov/webm/…
	KindAudio                     // mp3/ogg/wav/… (non-voice)
	KindVoice                     // Discord voice note (voice-message.ogg)
	KindFile                      // any other attachment (pdf/zip/txt/…)
	KindLink                      // an http(s) URL in the text
)

// KindMedia is the umbrella for "has any attachment".
const KindMedia = KindImage | KindVideo | KindAudio | KindVoice | KindFile

// classifyMessage derives the MsgKind bits from the message text and its
// space-separated attachment URLs.
func classifyMessage(content, attachments string) MsgKind {
	var k MsgKind
	atts := strings.Fields(attachments)
	for _, a := range atts {
		k |= attachmentKind(a)
	}
	if len(atts) == 0 && strings.TrimSpace(content) != "" {
		k |= KindText
	}
	if hasLink(content) {
		k |= KindLink
	}
	return k
}

// attachmentKind classifies a single attachment by the extension in its URL.
// Query/fragment suffixes (the signed ?ex=…&hm=… params on newer CDN URLs) are
// stripped first, so this stays fully offline.
func attachmentKind(u string) MsgKind {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	base := u
	if i := strings.LastIndexAny(u, "/\\"); i >= 0 {
		base = u[i+1:]
	}
	base = strings.ToLower(base)
	if base == "voice-message.ogg" {
		return KindVoice
	}
	ext := ""
	if i := strings.LastIndex(base, "."); i >= 0 {
		ext = base[i+1:]
	}
	switch ext {
	case "png", "jpg", "jpeg", "gif", "webp", "heic", "heif", "bmp", "tiff", "svg", "apng", "avif":
		return KindImage
	case "mp4", "mov", "webm", "mkv", "avi", "m4v", "wmv", "flv":
		return KindVideo
	case "mp3", "ogg", "oga", "wav", "flac", "m4a", "aac", "opus", "weba":
		return KindAudio
	default:
		return KindFile
	}
}

func hasLink(s string) bool {
	ls := strings.ToLower(s)
	return strings.Contains(ls, "http://") || strings.Contains(ls, "https://")
}

// typeIDMask maps a user-facing type id to its MsgKind mask. Unknown ids -> 0.
func typeIDMask(id string) MsgKind {
	switch id {
	case "text":
		return KindText
	case "media":
		return KindMedia
	case "image":
		return KindImage
	case "video":
		return KindVideo
	case "audio":
		return KindAudio
	case "voice":
		return KindVoice
	case "file":
		return KindFile
	case "link":
		return KindLink
	}
	return 0
}

// typesMask ORs together the masks of every selected type id (empty -> 0 = any).
func typesMask(sel map[string]bool) MsgKind {
	var m MsgKind
	for id, on := range sel {
		if on {
			m |= typeIDMask(id)
		}
	}
	return m
}

// typeOption is one selectable message-type filter, in display order.
type typeOption struct {
	id, label, short, desc string
}

var typeOptions = []typeOption{
	{"text", "Text only", "text", "Messages with text and no attachment."},
	{"media", "Media (any)", "media", "Any uploaded attachment: image, video, audio, voice, or file."},
	{"image", "Image", "image", "png, jpg, jpeg, gif, webp, heic, …"},
	{"video", "Video", "video", "mp4, mov, webm, mkv, …"},
	{"audio", "Audio", "audio", "mp3, ogg, wav, flac, m4a, … (excludes voice notes)."},
	{"voice", "Voice message", "voice", "Discord voice notes (voice-message.ogg)."},
	{"file", "File / other", "file", "Any other attachment: pdf, zip, txt, or anything not above."},
	{"link", "Contains a link", "link", "An http(s):// URL in the message text."},
}

// typeSelSummary renders the selected types compactly for a value cell.
func typeSelSummary(sel map[string]bool) string {
	var out []string
	for _, o := range typeOptions {
		if sel[o.id] {
			out = append(out, o.short)
		}
	}
	switch {
	case len(out) == 0:
		return "any"
	case len(out) <= 3:
		return strings.Join(out, ", ")
	default:
		return fmt.Sprintf("%d types", len(out))
	}
}

// describeTypes lists the set bits of a mask for the plain-run filter summary.
func describeTypes(k MsgKind) string {
	pairs := []struct {
		b MsgKind
		n string
	}{
		{KindText, "text"}, {KindImage, "image"}, {KindVideo, "video"},
		{KindAudio, "audio"}, {KindVoice, "voice"}, {KindFile, "file"}, {KindLink, "link"},
	}
	var out []string
	for _, p := range pairs {
		if k&p.b != 0 {
			out = append(out, p.n)
		}
	}
	return strings.Join(out, "/")
}

// countTypes tallies, per type id, how many messages across the package match:
// a static, offline count shown next to each option in the selector.
func countTypes(raws []RawChannel) map[string]int {
	// One pass tallying every type from the precomputed Kind bits, not one
	// full pass per type option (the package can hold 100k+ messages).
	type tm struct {
		id   string
		mask MsgKind
	}
	masks := make([]tm, 0, len(typeOptions))
	c := make(map[string]int, len(typeOptions))
	for _, o := range typeOptions {
		masks = append(masks, tm{o.id, typeIDMask(o.id)})
		c[o.id] = 0
	}
	for _, rc := range raws {
		for _, m := range rc.Messages {
			for _, t := range masks {
				if m.Kind&t.mask != 0 {
					c[t.id]++
				}
			}
		}
	}
	return c
}

// RawChannel is one channel's full, unfiltered message list plus the metadata
// needed to group and label it. The package is loaded into these ONCE; filters
// are then applied in memory via ApplyFilter.
type RawChannel struct {
	ChannelID string
	Label     string // "#general (My Server)" / "DM with Bob"
	Name      string // bare channel name, if known
	GuildID   string // "" for DMs
	GuildName string
	IsDM      bool
	Messages  []Message

	// ItemCount is the tally for synthetic channels with no Message data (the
	// reaction selector); 0 means count Messages. Read via items().
	ItemCount int
}

// items is the channel's deletable-item count: messages, or ItemCount for the
// synthetic reaction channels.
func (rc RawChannel) items() int {
	if rc.ItemCount > 0 {
		return rc.ItemCount
	}
	return len(rc.Messages)
}

// ChannelJob is one channel's worth of messages to delete (post-filter).
type ChannelJob struct {
	ChannelID string
	Label     string
	GuildID   string
	MsgIDs    []string     // message ids (message jobs)
	Items     []deleteItem // when non-nil, overrides MsgIDs (reaction jobs)
}

// deleteItem is one deletion the engine performs. Emoji "" means delete the
// message; otherwise it's the pre-encoded {emoji} path segment and the item is a
// reaction removal on MessageID. Key is the resume-log key recorded once gone.
type deleteItem struct {
	MessageID string
	Emoji     string
	Key       string
}

// count is the number of deletions in the job, from whichever field is set.
func (j ChannelJob) count() int {
	if j.Items != nil {
		return len(j.Items)
	}
	return len(j.MsgIDs)
}

// Filter narrows which messages get queued and controls deletion order.
type Filter struct {
	Guilds    map[string]bool // include-only guild ids (nil = all; non-nil empty = none)
	Channels  map[string]bool // include-only channel ids (nil = all; non-nil empty = none)
	Content   string          // substring, case-insensitive (empty = any)
	ContentRe *regexp.Regexp  // compiled regex; when set, used instead of Content
	AfterID   uint64          // snowflake; 0 = no lower bound
	BeforeID  uint64          // snowflake; 0 = no upper bound
	Order     string          // "oldest" (default) or "newest", per channel
	Types     MsgKind         // include only these kinds (0 = any); OR semantics
	Done      map[string]bool // message IDs already deleted in a prior run, skipped
}

// rawChannel mirrors the per-folder channel.json.
type rawChannel struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Type  int             `json:"type"`
	Guild json.RawMessage `json:"guild"`
}

func (r rawChannel) guildIDName() (string, string) {
	if len(r.Guild) == 0 || string(r.Guild) == "null" {
		return "", ""
	}
	var obj struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(r.Guild, &obj); err == nil && obj.ID != "" {
		return obj.ID, obj.Name
	}
	var s string
	if err := json.Unmarshal(r.Guild, &s); err == nil {
		return s, ""
	}
	return "", ""
}

// PackageOwner identifies whose account a data package belongs to, read from
// the package's own account/user.json. Used to make sure the token you're about
// to delete with is actually this package's account.
type PackageOwner struct {
	ID     string
	Name   string
	Handle string // unique @username
}

// LoadPackageOwner reads account/user.json from the package to learn whose
// account it is. Best-effort: ok=false if the file is absent or unparseable
// (older/partial exports), in which case the caller can't verify identity.
func LoadPackageOwner(pkgPath string) (PackageOwner, bool) {
	fsys, closer, err := openPackage(pkgPath)
	if err != nil {
		return PackageOwner{}, false
	}
	defer closer()
	return readPackageOwner(fsys)
}

func readPackageOwner(fsys fs.FS) (PackageOwner, bool) {
	// Find the owner file by walking the tree, exactly like the message loader,
	// not by exact root paths. Discord packages are sometimes nested under a
	// wrapper directory (a zip that wraps everything, or extraction into a
	// subfolder); an exact-path read would miss the owner while the recursive
	// message scan still succeeds, silently losing identity (and stable
	// resume/config keying). Prefer an `account/user.json` match, then any
	// `user.json`, shallowest first.
	var accountMatch, looseMatch string
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		lp := strings.ToLower(p)
		switch {
		case strings.HasSuffix(lp, "account/user.json"):
			if accountMatch == "" || len(p) < len(accountMatch) {
				accountMatch = p
			}
		case strings.ToLower(d.Name()) == "user.json":
			if looseMatch == "" || len(p) < len(looseMatch) {
				looseMatch = p
			}
		}
		return nil
	})
	for _, p := range []string{accountMatch, looseMatch} {
		if p == "" {
			continue
		}
		if owner, ok := parseOwnerFile(fsys, p); ok {
			return owner, true
		}
	}
	return PackageOwner{}, false
}

func parseOwnerFile(fsys fs.FS, p string) (PackageOwner, bool) {
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return PackageOwner{}, false
	}
	// Current exports store discriminator as a bare number ("0"), older ones as a
	// string; decode every field through firstString so a type mismatch on one
	// does not drop the whole owner and silently disable the account-match guard.
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return PackageOwner{}, false
	}
	id := firstString(raw, "id")
	if id == "" {
		return PackageOwner{}, false
	}
	username := firstString(raw, "username")
	discriminator := firstString(raw, "discriminator")
	global := firstString(raw, "global_name")
	return PackageOwner{
		ID:     id,
		Name:   friendlyUser(username, discriminator, global),
		Handle: uniqueHandle(username, discriminator),
	}, true
}

// LoadRawPackage reads a Discord data package (a .zip or an already-extracted
// folder) into per-channel raw message lists, without applying any filter.
// Filtering happens later, in memory, via ApplyFilter, so the TUI can
// re-preview instantly as the user changes the configuration.
func LoadRawPackage(pkgPath string) ([]RawChannel, error) {
	fsys, closer, err := openPackage(pkgPath)
	if err != nil {
		return nil, err
	}
	defer closer()
	raws := loadChannelsFS(fsys)
	if len(raws) == 0 {
		return nil, fmt.Errorf("no messages/*/messages.json (or .csv) found in %q; is this a Discord data package?", pkgPath)
	}
	return raws, nil
}

// ReadPackage loads a package once, returning what it can act on: messages,
// reactions, or both. It errors only when neither is present.
func ReadPackage(pkgPath string) (*LoadedPackage, error) {
	fsys, closer, err := openPackage(pkgPath)
	if err != nil {
		return nil, err
	}
	defer closer()
	reactions, err := loadReactions(fsys)
	if err != nil {
		return nil, err
	}
	p := &LoadedPackage{
		Raws:       loadChannelsFS(fsys),
		Reactions:  reactions,
		GuildNames: loadGuildNames(fsys),
	}
	// Owner comes from this same open package, so callers needn't reopen it.
	p.Owner, _ = readPackageOwner(fsys)
	p.Caps = PackageCapabilities{HasMessages: len(p.Raws) > 0, HasReactions: len(p.Reactions) > 0}
	if !p.Caps.HasMessages && !p.Caps.HasReactions {
		return nil, fmt.Errorf("no messages or reaction activity found in %q; is this a Discord data package?", pkgPath)
	}
	return p, nil
}

// loadChannelsFS walks a package filesystem and parses each channel's messages.
// Empty result (no messages) is not an error here; callers decide.
func loadChannelsFS(fsys fs.FS) []RawChannel {
	// Find each channel folder's message file. At most one per folder: if a
	// folder somehow ships both messages.json and messages.csv, taking both
	// would queue (and delete) every message twice. Prefer the JSON.
	byDir := map[string]string{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // skip unreadable entries rather than aborting the walk
		}
		base := strings.ToLower(d.Name())
		if base != "messages.json" && base != "messages.csv" {
			return nil
		}
		dir := path.Dir(p)
		existing, ok := byDir[dir]
		if !ok || (base == "messages.json" && strings.HasSuffix(strings.ToLower(existing), ".csv")) {
			byDir[dir] = p
		}
		return nil
	})
	if len(byDir) == 0 {
		return nil
	}

	msgFiles := make([]string, 0, len(byDir))
	for _, p := range byDir {
		msgFiles = append(msgFiles, p)
	}
	sort.Strings(msgFiles) // deterministic processing order

	var raws []RawChannel
	for _, mf := range msgFiles {
		rc, err := parseRawChannel(fsys, mf)
		if err != nil {
			// One bad channel shouldn't sink the load; note and continue.
			fmt.Fprintf(os.Stderr, "warn: %s: %v\n", mf, err)
			continue
		}
		if rc == nil || len(rc.Messages) == 0 {
			continue
		}
		raws = append(raws, *rc)
	}
	return raws
}

func parseRawChannel(fsys fs.FS, msgFile string) (*RawChannel, error) {
	dir := path.Dir(msgFile)

	// channel.json is optional; fall back to the folder name for the id.
	var ch rawChannel
	if data, err := fs.ReadFile(fsys, path.Join(dir, "channel.json")); err == nil {
		_ = json.Unmarshal(data, &ch)
	}
	channelID := ch.ID
	if channelID == "" {
		channelID = digitsOnly(path.Base(dir)) // e.g. "c1234567890" -> "1234567890"
	}
	if channelID == "" {
		return nil, fmt.Errorf("could not determine channel id")
	}
	guildID, guildName := ch.guildIDName()

	data, err := fs.ReadFile(fsys, msgFile)
	if err != nil {
		return nil, err
	}
	var msgs []Message
	if strings.HasSuffix(strings.ToLower(msgFile), ".csv") {
		msgs, err = parseMessagesCSV(data)
	} else {
		msgs, err = parseMessagesJSON(data)
	}
	if err != nil {
		return nil, err
	}

	return &RawChannel{
		ChannelID: channelID,
		Label:     channelLabel(ch, guildName, channelID),
		Name:      ch.Name,
		GuildID:   guildID,
		GuildName: guildName,
		IsDM:      guildID == "",
		Messages:  msgs,
	}, nil
}

// ApplyFilter turns raw channels into per-channel jobs, honoring the filter and
// per-channel deletion order. Runs entirely in memory, so it is cheap to call
// repeatedly as the user adjusts the configuration.
func ApplyFilter(raws []RawChannel, f Filter) ([]ChannelJob, int) {
	var jobs []ChannelJob
	total := 0
	for _, rc := range raws {
		// nil = no filter; a non-nil empty set means none selected, so
		// deselecting every channel must not widen back to "all".
		if f.Channels != nil && !f.Channels[rc.ChannelID] {
			continue
		}
		if f.Guilds != nil && !f.Guilds[rc.GuildID] {
			continue
		}
		ids := make([]string, 0, len(rc.Messages))
		for _, m := range rc.Messages {
			if keepMsg(m, f) {
				ids = append(ids, m.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		sortMsgIDs(ids, f.Order)
		jobs = append(jobs, ChannelJob{
			ChannelID: rc.ChannelID,
			Label:     rc.Label,
			GuildID:   rc.GuildID,
			MsgIDs:    ids,
		})
		total += len(ids)
	}
	// Biggest channels first: keeps all workers busy longest and stabilizes ETA.
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].count() > jobs[j].count()
	})
	return jobs, total
}

// LoadPackage is a convenience wrapper: load then filter in one call. Kept for
// the CLI preflight and tests.
func LoadPackage(pkgPath string, f Filter) ([]ChannelJob, int, error) {
	raws, err := LoadRawPackage(pkgPath)
	if err != nil {
		return nil, 0, err
	}
	jobs, total := ApplyFilter(raws, f)
	return jobs, total, nil
}

func keepMsg(m Message, f Filter) bool {
	if f.Done[m.ID] { // already deleted in a previous run, don't re-request
		return false
	}
	switch {
	case f.ContentRe != nil:
		if !f.ContentRe.MatchString(m.Content) {
			return false
		}
	case f.Content != "":
		if !strings.Contains(strings.ToLower(m.Content), strings.ToLower(f.Content)) {
			return false
		}
	}
	if f.Types != 0 && m.Kind&f.Types == 0 {
		return false
	}
	if f.AfterID != 0 || f.BeforeID != 0 {
		if !m.HasSnow {
			return false
		}
		if f.AfterID != 0 && m.Snowflake <= f.AfterID {
			return false
		}
		if f.BeforeID != 0 && m.Snowflake >= f.BeforeID {
			return false
		}
	}
	return true
}

// compileContentFilter interprets a content-filter string. A value wrapped in
// slashes (/pattern/ or /pattern/i) is a regular expression (the trailing i
// makes it case-insensitive); anything else is a plain case-insensitive
// substring. Returns (substring, regexp, error) with at most one of the first
// two set, so it round-trips cleanly through config and flags without a new
// field.
func compileContentFilter(s string) (string, *regexp.Regexp, error) {
	if len(s) < 2 || !strings.HasPrefix(s, "/") {
		return s, nil, nil
	}
	body := s[1:]
	ci := false
	switch {
	case strings.HasSuffix(body, "/i"):
		ci, body = true, body[:len(body)-2]
	case strings.HasSuffix(body, "/"):
		body = body[:len(body)-1]
	default:
		return s, nil, nil // no closing slash: treat as a substring
	}
	pat := body
	if ci {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return "", nil, fmt.Errorf("invalid content regex %q: %w", s, err)
	}
	return "", re, nil
}

// sortMsgIDs orders IDs by their encoded timestamp: "newest" = descending,
// anything else = oldest-first (ascending). Unparseable IDs sort to the end.
func sortMsgIDs(ids []string, order string) {
	type kv struct {
		id string
		n  uint64
		ok bool
	}
	decorated := make([]kv, len(ids))
	for i, id := range ids {
		n, err := strconv.ParseUint(id, 10, 64)
		decorated[i] = kv{id: id, n: n, ok: err == nil}
	}
	newest := order == "newest"
	sort.SliceStable(decorated, func(i, j int) bool {
		a, b := decorated[i], decorated[j]
		if a.ok != b.ok {
			return a.ok // valid IDs before unparseable ones
		}
		if newest {
			return a.n > b.n
		}
		return a.n < b.n
	})
	for i, d := range decorated {
		ids[i] = d.id
	}
}

// parseMessagesJSON handles the array-of-objects messages.json. Keys have been
// both TitleCase ("ID","Contents") and lowercase across package versions, so we
// accept either. Returns ALL messages; filtering happens in ApplyFilter.
func parseMessagesJSON(data []byte) ([]Message, error) {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("messages.json: %w", err)
	}
	out := make([]Message, 0, len(rows))
	for _, row := range rows {
		id := firstString(row, "ID", "id", "Id")
		if id == "" {
			continue
		}
		content := firstString(row, "Contents", "content", "Content")
		out = append(out, newMessageFull(id, content, attachmentsField(row)))
	}
	return out, nil
}

// attachmentsField extracts the attachment URLs from a message row, tolerating
// the several shapes seen across package versions: a single space-separated
// string, an array of URL strings, or an array of objects with a url/filename.
// Returns them space-joined for classifyMessage.
func attachmentsField(row map[string]json.RawMessage) string {
	for _, k := range []string{"Attachments", "attachments", "Attachment"} {
		raw, ok := row[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		var arrS []string
		if json.Unmarshal(raw, &arrS) == nil {
			return strings.Join(arrS, " ")
		}
		var arrO []map[string]json.RawMessage
		if json.Unmarshal(raw, &arrO) == nil {
			var urls []string
			for _, o := range arrO {
				if u := firstString(o, "url", "URL", "proxy_url", "filename", "name"); u != "" {
					urls = append(urls, u)
				}
			}
			return strings.Join(urls, " ")
		}
	}
	return ""
}

func parseMessagesCSV(data []byte) ([]Message, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("messages.csv: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	idCol, contentCol, attCol := -1, -1, -1
	for i, h := range rows[0] {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "id":
			idCol = i
		case "contents", "content":
			contentCol = i
		case "attachments", "attachment":
			attCol = i
		}
	}
	if idCol == -1 {
		return nil, fmt.Errorf("messages.csv: no ID column")
	}
	out := make([]Message, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if idCol >= len(row) {
			continue
		}
		id := strings.TrimSpace(row[idCol])
		if id == "" {
			continue
		}
		contents := ""
		if contentCol >= 0 && contentCol < len(row) {
			contents = row[contentCol]
		}
		atts := ""
		if attCol >= 0 && attCol < len(row) {
			atts = row[attCol]
		}
		out = append(out, newMessageFull(id, contents, atts))
	}
	return out, nil
}

// newMessage builds a text-only Message (no attachments). Kept for tests/callers
// that don't care about media classification.
func newMessage(id, content string) Message {
	return newMessageFull(id, content, "")
}

func newMessageFull(id, content, attachments string) Message {
	n, err := strconv.ParseUint(id, 10, 64)
	return Message{
		ID:        id,
		Snowflake: n,
		HasSnow:   err == nil,
		Content:   content,
		Kind:      classifyMessage(content, attachments),
	}
}

func channelLabel(ch rawChannel, guildName, channelID string) string {
	switch {
	case ch.Name != "" && guildName != "":
		return fmt.Sprintf("#%s (%s)", ch.Name, guildName)
	case ch.Name != "":
		return "#" + ch.Name
	case guildName != "":
		return guildName + " / " + channelID
	default:
		return "DM/channel " + channelID
	}
}

func openPackage(p string) (fs.FS, func(), error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, nil, err
	}
	if info.IsDir() {
		return os.DirFS(p), func() {}, nil
	}
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, nil, fmt.Errorf("open %q as zip: %w", p, err)
	}
	// The package is only ever read, so a close error is not actionable.
	return zr, func() { _ = zr.Close() }, nil
}

func firstString(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		if raw, ok := m[k]; ok {
			// value may be a JSON string or a bare number
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				return s
			}
			var n json.Number
			if err := json.Unmarshal(raw, &n); err == nil {
				return n.String()
			}
		}
	}
	return ""
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
