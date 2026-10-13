package verbs

import (
	"os"
	"testing"
)

// TestMain points the home directory at an empty temp dir so no verb under
// test reads the developer's saved token or reaches a real API: status asks
// gears-api for the project list (GRS-46), and every card write pushes and
// every card read pulls at once (GRS-51). Tests that want an API log in to
// a fake one (fakeapi.Login).
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "gears-verbs-home-")
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
