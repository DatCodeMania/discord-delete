package main

import (
	"strings"
	"testing"
	"time"
)

func sampleReport(execute bool, aborted bool) runReport {
	snap := Snapshot{
		Total: 100, Deleted: 90, Skipped: 8, Failed: 2,
		Elapsed: 3 * time.Minute, Aborted: aborted, Finished: true, Completed: !aborted,
		Workers: make([]WorkerStatus, 4), ActiveLimit: 1,
	}
	return runReport{
		Package: "pkg.zip", Execute: execute,
		StartedAt: time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 7, 9, 10, 3, 0, 0, time.UTC),
		Results:   []opResult{{Kind: "messages", Snap: snap, Collapsed: true}},
		Resumed:   5,
	}
}

func TestReportText(t *testing.T) {
	txt := sampleReport(true, false).text()
	for _, want := range []string{"pkg.zip", "completed", "deleted", "90", "single worker", "resumed"} {
		if !strings.Contains(txt, want) {
			t.Errorf("report text missing %q:\n%s", want, txt)
		}
	}
}

func TestReportDryRunWording(t *testing.T) {
	txt := sampleReport(false, false).text()
	if !strings.Contains(txt, "dry run") || !strings.Contains(txt, "nothing was deleted") {
		t.Errorf("dry-run report should say nothing was deleted:\n%s", txt)
	}
}

func TestReportNotifyFields(t *testing.T) {
	ok := sampleReport(true, false)
	if got := ok.notifyTitle(); got != "discord-delete delete completed" {
		t.Errorf("title = %q", got)
	}
	// Notifications carry no emoji tags: priority conveys severity.
	if ok.notifyTags() != "" || ok.notifyPriority() != "default" {
		t.Errorf("healthy run: no tags, default priority")
	}
	bad := sampleReport(true, true)
	if bad.notifyTags() != "" || bad.notifyPriority() != "high" {
		t.Errorf("aborted run: no tags, high priority")
	}
	// Body carries counts, never content.
	if !strings.Contains(ok.notifyBody(), "90 deleted") {
		t.Errorf("body should summarize counts: %q", ok.notifyBody())
	}
}

// TestReportStoppedVsCompleted verifies a run that returned without
// processing everything (user stop) reports "stopped", not "completed".
func TestReportStoppedVsCompleted(t *testing.T) {
	stopped := sampleReport(true, false)
	stopped.Results[0].Snap.Completed = false // Finished=true but not all jobs done
	if got := stopped.status(); got != "stopped" {
		t.Errorf("status = %q, want stopped", got)
	}
	if got := stopped.notifyTitle(); got != "discord-delete delete stopped" {
		t.Errorf("title = %q, want ...stopped", got)
	}
	if stopped.notifyTags() != "" {
		t.Error("notifications carry no emoji tags")
	}
	if !strings.Contains(stopped.notifyBody(), "Re-run to resume") {
		t.Errorf("stopped body should hint at resuming: %q", stopped.notifyBody())
	}
}

func TestForbiddenByServer(t *testing.T) {
	raws := []RawChannel{
		{ChannelID: "a1", GuildID: "g1", GuildName: "Left Server"},
		{ChannelID: "a2", GuildID: "g1", GuildName: "Left Server"},
		{ChannelID: "b1", GuildID: "g2", GuildName: "Joined Server"},
		{ChannelID: "d1", IsDM: true, Label: "DM with Bob"},
		{ChannelID: "d2", IsDM: true, Label: "DM with Alice"},
	}
	forbidden := map[string]ForbiddenStat{
		"a1": {Count: 10, Reason: "Missing Permissions"},
		"a2": {Count: 5, Reason: "Missing Permissions"},
		"b1": {Count: 3, Reason: "Cannot execute action on a system message"},
		"d1": {Count: 2, Reason: "Cannot execute action on a system message"},
		"d2": {Count: 7, Reason: "Missing Access"},
	}
	members := map[string]bool{"g2": true} // still in g2, left g1

	got := forbiddenByServer(forbidden, metaFromRaws(raws), members)
	// Servers collapse to one line each; DMs get one line apiece: 4 total.
	if len(got) != 4 {
		t.Fatalf("want 4 groups (2 servers + 2 DMs), got %d: %+v", len(got), got)
	}
	// Sorted by message count desc: Left Server (15) first.
	if got[0].Server != "Left Server" || got[0].Messages != 15 || got[0].Channels != 2 {
		t.Errorf("top group wrong: %+v", got[0])
	}
	if got[0].Member != memberNo {
		t.Errorf("left server should be labeled memberNo, got %v", got[0].Member)
	}
	if got[0].ChannelID != "" {
		t.Errorf("an aggregated server should not carry a single ChannelID, got %q", got[0].ChannelID)
	}
	var joined *forbiddenServer
	dms := map[string]*forbiddenServer{}
	for i := range got {
		switch {
		case got[i].Server == "Joined Server":
			joined = &got[i]
		case got[i].IsDM:
			dms[got[i].ChannelID] = &got[i]
		}
	}
	if joined == nil || joined.Member != memberYes {
		t.Errorf("joined server should be memberYes, got %+v", joined)
	}
	if len(dms) != 2 {
		t.Fatalf("each DM should be its own entry, got %d: %+v", len(dms), dms)
	}
	if dms["d1"] == nil || dms["d1"].Messages != 2 || dms["d1"].Server != "DM with Bob" {
		t.Errorf("DM d1 wrong: %+v", dms["d1"])
	}
	if dms["d1"].Reason != "Cannot execute action on a system message" {
		t.Errorf("DM d1 should carry Discord's reason, got %q", dms["d1"].Reason)
	}
	if dms["d2"] == nil || dms["d2"].Messages != 7 || dms["d2"].Reason != "Missing Access" {
		t.Errorf("DM d2 wrong: %+v", dms["d2"])
	}
	// The left server saw only "Missing Permissions" across both channels.
	if got[0].Reason != "Missing Permissions" {
		t.Errorf("left server reason = %q, want Missing Permissions", got[0].Reason)
	}
	// No membership map -> no labels.
	unlabeled := forbiddenByServer(forbidden, metaFromRaws(raws), nil)
	for _, fs := range unlabeled {
		if fs.Member != memberUnknown {
			t.Errorf("without a members map, everything should be memberUnknown: %+v", fs)
		}
	}
}

// TestReportRendersReasonAndChannel confirms a DM shows its channel ID and the
// reason Discord gave, so the user can tell recoverable from permanent.
func TestReportRendersReasonAndChannel(t *testing.T) {
	r := sampleReport(true, false)
	r.Results[0].Forbidden = []forbiddenServer{
		{Server: "Left Server", Messages: 900, Channels: 2, Member: memberNo, Reason: "Missing Access"},
		{Server: "DM with Bob", IsDM: true, ChannelID: "555000111", Messages: 12,
			Reason: "Cannot execute action on a system message"},
	}
	txt := r.text()
	if !strings.Contains(txt, "channel 555000111") {
		t.Errorf("DM should show its channel ID:\n%s", txt)
	}
	if !strings.Contains(txt, "reason: Cannot execute action on a system message") {
		t.Errorf("report should show Discord's reason:\n%s", txt)
	}
	if !strings.Contains(txt, "re-run") {
		t.Errorf("report should explain the regain-access-then-rerun path:\n%s", txt)
	}
}

func TestReportDestPath(t *testing.T) {
	r := sampleReport(true, false)
	// Override wins verbatim.
	if got := r.destPath("/tmp/my.txt", "/state/user-1.deleted.log"); got != "/tmp/my.txt" {
		t.Errorf("override should win, got %q", got)
	}
	// Otherwise derived beside the resume log, timestamped, .report.txt.
	got := r.destPath("", "/state/user-1.deleted.log")
	if !strings.HasPrefix(got, "/state/user-1-") || !strings.HasSuffix(got, ".report.txt") {
		t.Errorf("derived path unexpected: %q", got)
	}
	// No progPath and no override -> nothing to write.
	if got := r.destPath("", ""); got != "" {
		t.Errorf("expected empty dest with no path, got %q", got)
	}
}
