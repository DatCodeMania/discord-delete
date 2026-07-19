package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPackageOwner(t *testing.T) {
	root := t.TempDir()
	acct := filepath.Join(root, "account")
	if err := os.MkdirAll(acct, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(acct, "user.json"),
		`{"id":"111222333","username":"user1","discriminator":"0","global_name":"User One"}`)

	owner, ok := LoadPackageOwner(root)
	if !ok {
		t.Fatal("expected to read the package owner")
	}
	if owner.ID != "111222333" {
		t.Fatalf("owner id: want 111222333, got %q", owner.ID)
	}
	if owner.Name != "User One" {
		t.Fatalf("owner name: want 'User One', got %q", owner.Name)
	}
}

func TestReadPackageOwnerMissing(t *testing.T) {
	root := t.TempDir() // no account/user.json
	if _, ok := LoadPackageOwner(root); ok {
		t.Fatal("expected ok=false when account/user.json is absent")
	}
}

// A package nested under a wrapper directory (common with wrapped zips or
// extraction into a subfolder) must still yield its owner. The message loader
// walks the tree, so the owner reader must too, or identity and keying is lost.
func TestReadPackageOwnerNested(t *testing.T) {
	root := t.TempDir()
	acct := filepath.Join(root, "my-package", "account")
	if err := os.MkdirAll(acct, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(acct, "user.json"),
		`{"id":"444555666","username":"nested","discriminator":"0","global_name":"Nested User"}`)

	owner, ok := LoadPackageOwner(root)
	if !ok {
		t.Fatal("expected to read the owner from a nested package")
	}
	if owner.ID != "444555666" {
		t.Fatalf("owner id: want 444555666, got %q", owner.ID)
	}
}

func TestOwnerMismatch(t *testing.T) {
	m := demoModel()

	m.ownerID, m.tokenID = "", "999"
	if m.ownerMismatch() {
		t.Fatal("unknown owner must not count as a mismatch")
	}
	m.ownerID, m.tokenID = "111", ""
	if m.ownerMismatch() {
		t.Fatal("unknown token id must not count as a mismatch")
	}
	m.ownerID, m.tokenID = "111", "111"
	if m.ownerMismatch() {
		t.Fatal("matching ids must not be a mismatch")
	}
	m.ownerID, m.tokenID = "111", "222"
	if !m.ownerMismatch() {
		t.Fatal("different ids must be a mismatch")
	}
}

func TestMismatchBlocksExecute(t *testing.T) {
	m := demoModel()
	m.cfg.token = "sometoken"
	m.tokenState, m.tokenUser, m.tokenID = tsValid, "user2", "222"
	m.ownerID, m.ownerName = "111", "user1"

	m.toggleExecute()
	if m.cfg.execute {
		t.Fatal("execute must be refused on an owner mismatch")
	}
	if m.perr == "" {
		t.Fatal("expected an explanatory error on the mismatch")
	}

	// Directly forcing execute=true and starting a run must still be refused, not reach confirm.
	m.cfg.execute = true
	m.screen = scHome
	m.startRun()
	if m.screen == scConfirm {
		t.Fatal("start must not proceed to confirm on an owner mismatch")
	}
}
