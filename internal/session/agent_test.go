package session

import "testing"

// GRS-21: the acting agent and where it came from, strongest first.
func TestResolveAgent(t *testing.T) {
	defer func(f func() string) { LoginLabel = f }(LoginLabel)
	defer func(f func(string) string) { WorkspaceAgent = f }(WorkspaceAgent)
	t.Setenv("GEARS_AGENT", "")
	LoginLabel = nil
	WorkspaceAgent = nil
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
	// GRS-63: the workspace's saved name sits between GEARS_AGENT and the
	// login label, and is looked up for the directory given.
	var asked string
	WorkspaceAgent = func(dir string) string { asked = dir; return " ws-name " }
	if l, s := ResolveAgentAt("", "/some/ws"); l != "ws-name" || s != AgentFromWorkspace || asked != "/some/ws" {
		t.Fatalf("workspace: %s %s (asked %q)", l, s, asked)
	}
	if DefaultAgent() != "ws-name" {
		t.Fatal("DefaultAgent ignores the workspace name")
	}
	t.Setenv("GEARS_AGENT", "claude-local")
	if l, s := ResolveAgent(""); l != "claude-local" || s != AgentFromEnv {
		t.Fatalf("env: %s %s", l, s)
	}
	if l, s := ResolveAgent("  grok "); l != "grok" || s != AgentFromFlag {
		t.Fatalf("flag: %s %s", l, s)
	}
	LoginLabel = func() string { return "" }
	WorkspaceAgent = func(string) string { return "" }
	t.Setenv("GEARS_AGENT", " ")
	if l, s := ResolveAgent(""); l != "local" || s != AgentFromDefault {
		t.Fatalf("blank env and login: %s %s", l, s)
	}
}
