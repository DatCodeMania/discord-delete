package main

import (
	"strings"
	"testing"
)

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

// The Remember field reports what the keyring holds, not just the flag: on with
// nothing saved, on with this token saved, and on after a save that failed.
func TestRememberFieldReflectsStoredToken(t *testing.T) {
	stubKeyring(t, true)
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	m := demoModel()
	m.stateKey = "user-99"
	m.cfg.remember = true
	m.cfg.token = "tok-abc"
	m.tokenState = tsChecking

	if got := m.fieldValue("remember"); got != "on (not saved yet)" {
		t.Fatalf("remember on with nothing stored: got %q", got)
	}

	m.tokenState = tsValid
	m.maybeSaveToken()
	if got := m.fieldValue("remember"); got != "on (saved for this account)" {
		t.Fatalf("remember on with the token stored: got %q", got)
	}

	stubKeyring(t, false)
	m.cfg.token = "tok-def"
	m.maybeSaveToken()
	if got := m.fieldValue("remember"); got != "on (save failed, see guide)" {
		t.Fatalf("remember on after a failed save: got %q", got)
	}
	if !strings.HasPrefix(m.storeNote, "not remembered:") {
		t.Fatalf("the guide note should say the save failed, got %q", m.storeNote)
	}

	m.cfg.token = "tok-ghi"
	m.startTokenCheck()
	if got := m.fieldValue("remember"); got != "on (not saved yet)" {
		t.Fatalf("a fresh token must not inherit the failure: got %q", got)
	}
}

// A 401 abort forgets the stored token only when the stored token is the one
// the run was using; a bad token passed over a good stored one must keep it.
func TestAbortForgetsOnlyTheTokenInUse(t *testing.T) {
	stubKeyring(t, true)
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	m := demoModel()
	m.stateKey = "user-55"
	if _, err := saveToken("user-55", "stored-tok"); err != nil {
		t.Fatal(err)
	}
	m.savedToken = "stored-tok"
	m.cfg.token = "different-tok"
	m.stats = NewStats(1, 1)
	m.stats.aborted.Store(true)
	m.finishRun()
	if !hasStoredToken("user-55") {
		t.Fatal("an abort on a non-stored token must keep the stored one")
	}

	m.cfg.token = "stored-tok"
	m.finishRun()
	if hasStoredToken("user-55") {
		t.Fatal("an abort on the stored token should forget it")
	}
	if m.savedToken != "" || m.tokenFromStore {
		t.Fatal("forgetting should clear the saved-token bookkeeping")
	}
}

// An explicit --remember wins over the saved config both ways; without it the
// saved value is restored.
func TestRememberFlagBeatsPersisted(t *testing.T) {
	for _, tc := range []struct {
		name     string
		passed   bool
		flagVal  bool
		savedVal bool
		want     bool
	}{
		{"explicit false over saved on", true, false, true, false},
		{"explicit true over saved off", true, true, false, true},
		{"absent restores saved on", false, false, true, true},
		{"absent restores saved off", false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultRunConfig()
			cfg.remember = tc.flagVal
			setFlags := map[string]bool{}
			if tc.passed {
				setFlags["remember"] = true
			}
			applyPersisted(&cfg, map[string]bool{}, persistedConfig{Remember: tc.savedVal}, setFlags, nil)
			if cfg.remember != tc.want {
				t.Fatalf("remember = %v, want %v", cfg.remember, tc.want)
			}
		})
	}
}

// --remember=false declines to save without touching what is stored.
func TestRememberFalseKeepsOneOffTokenOutOfKeyring(t *testing.T) {
	store := stubKeyring(t, true)
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())
	if _, err := saveToken("user-9", "stored-tok"); err != nil {
		t.Fatal(err)
	}

	cfg := defaultRunConfig()
	applyPersisted(&cfg, map[string]bool{}, persistedConfig{Remember: true}, map[string]bool{"remember": true}, nil)

	m := demoModel()
	m.stateKey = "user-9"
	m.cfg.remember = cfg.remember
	m.cfg.token = "one-off-tok"
	m.applyTokenCheck(tokenCheckMsg{token: "one-off-tok", state: tsValid})

	if store["user-9"] != "stored-tok" {
		t.Fatalf("keyring holds %q, want the stored token untouched", store["user-9"])
	}
}

// --remember=true arms saving even when the saved config is off.
func TestRememberTrueArmsSavingOverPersistedOff(t *testing.T) {
	stubKeyring(t, true)
	t.Setenv("DISCORD_DELETE_STATE_DIR", t.TempDir())

	cfg := defaultRunConfig()
	cfg.remember = true
	applyPersisted(&cfg, map[string]bool{}, persistedConfig{Remember: false}, map[string]bool{"remember": true}, nil)

	m := demoModel()
	m.stateKey = "user-10"
	m.cfg.remember = cfg.remember
	m.cfg.token = "fresh-tok"
	m.applyTokenCheck(tokenCheckMsg{token: "fresh-tok", state: tsValid})

	if !hasStoredToken("user-10") {
		t.Fatal("a validated token should be stored when --remember=true")
	}
}
