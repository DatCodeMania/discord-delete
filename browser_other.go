//go:build !windows

package main

// chromeFromRegistry is a no-op off Windows, where findChrome has only PATH and
// the install-path list to work with.
func chromeFromRegistry() string { return "" }
