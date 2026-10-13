//go:build !windows

package cli

// enableVT: terminals outside Windows read ANSI escapes as they are.
func enableVT() bool { return true }
