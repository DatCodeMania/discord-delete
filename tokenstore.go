package main

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// Optional, opt-in token storage. The token is a full-account credential, so
// it is only ever stored in the OS keyring (macOS Keychain, Windows Credential
// Manager, Linux Secret Service), which encrypts it at rest and ties it to
// your login. Never written to a plaintext file or the per-package config.
// Without a keyring (e.g. a headless box with no secret service), storage is
// unavailable and the token stays in memory only. Stored per account, keyed
// the same as the resume log (user-<id> when known).
const keyringService = "discord-delete"

// Indirected for tests (no real keyring in CI).
var (
	keyringSet    = keyring.Set
	keyringGet    = keyring.Get
	keyringDelete = keyring.Delete
)

func saveToken(key, token string) (backend string, err error) {
	// Keep the real keyring error instead of a fixed "no keyring" message.
	if err := keyringSet(keyringService, key, token); err != nil {
		return "", fmt.Errorf("keyring save failed: %w", err)
	}
	return "keychain", nil
}

func loadToken(key string) (token, backend string, ok bool) {
	v, err := keyringGet(keyringService, key)
	if err != nil || v == "" {
		return "", "", false
	}
	return v, "keychain", true
}

// forgetToken removes any stored token for the key. A missing entry is fine;
// any other failure is returned, so callers don't report "removed" falsely.
func forgetToken(key string) error {
	if err := keyringDelete(keyringService, key); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// hasStoredToken reports whether a token is stored for the key, so the UI can
// offer to forget it.
func hasStoredToken(key string) bool {
	_, err := keyringGet(keyringService, key)
	return err == nil
}
