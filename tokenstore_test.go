package main

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

// stubKeyring swaps the keyring funcs for an in-memory map for the duration of a
// test, and can simulate an unavailable backend.
func stubKeyring(t *testing.T, available bool) map[string]string {
	t.Helper()
	m := map[string]string{}
	origSet, origGet, origDel := keyringSet, keyringGet, keyringDelete
	t.Cleanup(func() { keyringSet, keyringGet, keyringDelete = origSet, origGet, origDel })
	if !available {
		unavailable := errors.New("no secret service")
		keyringSet = func(_, _, _ string) error { return unavailable }
		keyringGet = func(_, _ string) (string, error) { return "", unavailable }
		keyringDelete = func(_, _ string) error { return unavailable }
		return m
	}
	keyringSet = func(_, user, pw string) error { m[user] = pw; return nil }
	keyringGet = func(_, user string) (string, error) {
		if v, ok := m[user]; ok {
			return v, nil
		}
		return "", keyring.ErrNotFound
	}
	keyringDelete = func(_, user string) error {
		if _, ok := m[user]; !ok {
			return keyring.ErrNotFound
		}
		delete(m, user)
		return nil
	}
	return m
}

func TestSaveLoadViaKeychain(t *testing.T) {
	stubKeyring(t, true)
	backend, err := saveToken("user-1", "tokA")
	if err != nil || backend != "keychain" {
		t.Fatalf("save via keychain: backend=%q err=%v", backend, err)
	}
	if !hasStoredToken("user-1") {
		t.Fatal("hasStoredToken should be true after save")
	}
	tok, backend, ok := loadToken("user-1")
	if !ok || tok != "tokA" || backend != "keychain" {
		t.Fatalf("load via keychain: tok=%q backend=%q ok=%v", tok, backend, ok)
	}
	if err := forgetToken("user-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := loadToken("user-1"); ok {
		t.Fatal("token should be gone after forget")
	}
	if hasStoredToken("user-1") {
		t.Fatal("hasStoredToken should be false after forget")
	}
}

func TestSaveWithoutKeyringErrors(t *testing.T) {
	stubKeyring(t, false)
	if _, err := saveToken("user-2", "tokB"); err == nil {
		t.Fatal("expected an error when no secret service is available")
	}
	if _, _, ok := loadToken("user-2"); ok {
		t.Fatal("nothing should load when the keyring is unavailable")
	}
}

// A failing keyring delete must surface as an error so callers never report
// "removed" while the credential is still stored.
func TestForgetTokenSurfacesKeyringFailure(t *testing.T) {
	stubKeyring(t, false)
	if err := forgetToken("user-3"); err == nil {
		t.Fatal("forgetToken should return the keyring error when delete fails")
	}
}
