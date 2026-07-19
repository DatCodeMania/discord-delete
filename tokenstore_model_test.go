package main

import "testing"

// A token is stored only when remember is on and it validated, and cleared on forget.
func TestModelRemembersAndForgetsToken(t *testing.T) {
	stubKeyring(t, true)
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	m := demoModel()
	m.stateKey = "user-42"
	m.cfg.token = "tok-xyz"

	// Not remembered yet: a valid token with remember off must not be stored.
	m.tokenState = tsValid
	m.maybeSaveToken()
	if hasStoredToken("user-42") {
		t.Fatal("token must not be stored while remember is off")
	}

	m.cfg.remember = true
	m.maybeSaveToken()
	if !hasStoredToken("user-42") {
		t.Fatal("token should be stored once remember is on and it validated")
	}

	// Forget clears it and resets provenance.
	m.tokenFromStore = true
	m.forgetStoredToken()
	if hasStoredToken("user-42") {
		t.Fatal("forget should remove the stored token")
	}
	if m.tokenFromStore {
		t.Fatal("forget should clear tokenFromStore")
	}
}

// A token that hasn't validated must never be stored, even with remember on.
func TestModelDoesNotStoreUnvalidatedToken(t *testing.T) {
	stubKeyring(t, true)
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	m := demoModel()
	m.stateKey = "user-77"
	m.cfg.remember = true
	m.cfg.token = "unchecked"
	m.tokenState = tsChecking

	m.maybeSaveToken()
	if hasStoredToken("user-77") {
		t.Fatal("an unvalidated token must not be persisted")
	}
}

// applyTokenCheck triggers a save when the probe comes back valid.
func TestApplyTokenCheckPersistsWhenValid(t *testing.T) {
	stubKeyring(t, true)
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	m := demoModel()
	m.stateKey = "user-88"
	m.cfg.remember = true
	m.cfg.token = "good-token"

	m.applyTokenCheck(tokenCheckMsg{token: "good-token", state: tsValid, user: "me", userID: "88"})
	if !hasStoredToken("user-88") {
		t.Fatal("a validated token should be persisted via applyTokenCheck")
	}
}
