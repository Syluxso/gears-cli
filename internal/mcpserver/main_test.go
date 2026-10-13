package mcpserver

import (
	"os"
	"testing"
)

// TestMain points the home directory at an empty temp dir so no tool under
// test (status asks gears-api for the project list, GRS-46) reads the
// developer's saved token or reaches a real API.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "gears-mcp-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	os.Setenv("GEARS_API", "")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
