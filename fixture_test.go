package main

import "testing"

// The testdata/packages/* fixtures mirror the real current Discord export format
// (numeric discriminator, string channel "type", capitalized message field names).

func TestFixtureModernPackageOwner(t *testing.T) {
	owner, ok := LoadPackageOwner("testdata/packages/modern")
	if !ok {
		t.Fatal("expected to read owner from the modern fixture")
	}
	if owner.ID != "100000000000000001" {
		t.Fatalf("owner id: got %q", owner.ID)
	}
	if owner.Handle != "testowner" {
		t.Fatalf("owner handle: got %q", owner.Handle)
	}
}

func TestFixtureModernPackageMessages(t *testing.T) {
	raws, err := LoadRawPackage("testdata/packages/modern")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	total := 0
	ids := map[string]bool{}
	for _, rc := range raws {
		for _, m := range rc.Messages {
			total++
			ids[m.ID] = true
		}
	}
	if len(raws) != 2 {
		t.Fatalf("channels: want 2, got %d", len(raws))
	}
	if total != 3 {
		t.Fatalf("messages: want 3, got %d", total)
	}
	// Fields are capitalized (ID/Contents) in real exports; the parser must read them.
	if !ids["1118691341107200000"] || !ids["1135723570790400000"] {
		t.Fatalf("expected the fixture message ids to parse, got %v", ids)
	}
}
