package session

import "testing"

// GRS-21: the acting agent and where it came from, strongest first.
func TestResolveAgent(t *testing.T) {
	defer func(f func() string) { LoginLabel = f }(LoginLabel)
	t.Setenv("GEARS_AGENT", "")
	LoginLabel = nil
	if l, s := ResolveAgent(""); l != "local" || s != AgentFromDefault {
		t.Fatalf("default: %s %s", l, s)
	}
	LoginLabel = func() string { return " codex " }
	if l, s := ResolveAgent(""); l != "codex" || s != AgentFromLogin {
		t.Fatalf("login: %s %s", l, s)
	}
	if DefaultAgent() != "codex" {
		t.Fatal("DefaultAgent ignores the login label")
	}
	t.Setenv("GEARS_AGENT", "claude-local")
	if l, s := ResolveAgent(""); l != "claude-local" || s != AgentFromEnv {
		t.Fatalf("env: %s %s", l, s)
	}
	if l, s := ResolveAgent("  grok "); l != "grok" || s != AgentFromFlag {
		t.Fatalf("flag: %s %s", l, s)
	}
	LoginLabel = func() string { return "" }
	t.Setenv("GEARS_AGENT", " ")
	if l, s := ResolveAgent(""); l != "local" || s != AgentFromDefault {
		t.Fatalf("blank env and login: %s %s", l, s)
	}
}
