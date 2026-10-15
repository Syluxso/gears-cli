package project

import "testing"

// GRS-63: agent names are 1-64 of a-z 0-9 - _ . +.
func TestValidateAgentName(t *testing.T) {
	for _, ok := range []string{"claude-local", "claude-code+local", "a", "grok_build.v2"} {
		if err := ValidateAgentName(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Claude", "has space", "a/b", string(make([]byte, 65))} {
		if ValidateAgentName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	root := t.TempDir()
	if ReadAgentName(root) != "" {
		t.Fatal("name before any was saved")
	}
	if err := WriteAgentName(root, "claude-local"); err != nil {
		t.Fatal(err)
	}
	if got := ReadAgentName(root); got != "claude-local" {
		t.Fatalf("read back %q", got)
	}
}
