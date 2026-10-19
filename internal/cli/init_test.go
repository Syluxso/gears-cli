package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
)

// initEnv isolates one init test: a temp HOME, no GEARS_AGENT, no flag
// values left over from an earlier Execute, piped stdin with the given
// lines, and the terminal seam set to tty.
func initEnv(t *testing.T, tty bool, input string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GEARS_API", "http://127.0.0.1:1")
	t.Setenv("GEARS_AGENT", "")
	resetInitFlags()
	t.Cleanup(resetInitFlags)
	setInput(t, tty, input)
}

func setInput(t *testing.T, tty bool, input string) {
	t.Helper()
	oldIn, oldTTY := stdin, stdinIsTerminal
	stdin = bufio.NewReader(strings.NewReader(input))
	stdinIsTerminal = func() bool { return tty }
	t.Cleanup(func() { stdin, stdinIsTerminal = oldIn, oldTTY })
}

func resetInitFlags() {
	initName, initSlug, initPrefix, globalAgent = "", "", "", ""
	initHook, hooksDir = false, ""
}

func savedName(t *testing.T, ws string) string {
	t.Helper()
	data, err := os.ReadFile(project.AgentPathAt(ws))
	if err != nil {
		t.Fatalf("no saved agent name: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// GRS-63: no name and no terminal: refused before anything is created.
func TestInitNonInteractiveRefusesAndCreatesNothing(t *testing.T) {
	initEnv(t, false, "")
	ws := t.TempDir()
	err := runIn(t, ws, "init")
	if err == nil || !strings.Contains(err.Error(), "gears init --name <label>") || !strings.Contains(err.Error(), "GEARS_AGENT") {
		t.Fatalf("want a refusal naming --name and GEARS_AGENT, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, project.Dir)); !os.IsNotExist(err) {
		t.Fatalf(".gears created on refusal: %v", err)
	}
	// An invalid --name is refused the same way.
	initName = ""
	if err := runIn(t, ws, "init", "--name", "Bad Name"); err == nil {
		t.Fatal("invalid --name accepted")
	}
	if _, err := os.Stat(filepath.Join(ws, project.Dir)); !os.IsNotExist(err) {
		t.Fatalf(".gears created on an invalid name: %v", err)
	}
}

// GRS-63: on a terminal init asks first, re-asks on an invalid name, saves
// the answer; a re-run keeps it without asking; ResolveAgent then uses it.
func TestInitPromptsThenKeepsSavedName(t *testing.T) {
	initEnv(t, true, "Not Valid\nclaude-test\n")
	ws := t.TempDir()
	if err := runIn(t, ws, "init"); err != nil {
		t.Fatal(err)
	}
	if got := savedName(t, ws); got != "claude-test" {
		t.Fatalf("saved %q", got)
	}
	if l, s := session.ResolveAgentAt("", ws); l != "claude-test" || s != session.AgentFromWorkspace {
		t.Fatalf("resolve after init: %s %s", l, s)
	}

	// Re-run: no input at all, so a prompt would fail; the saved name is kept,
	// even with GEARS_AGENT set (only --name replaces it).
	setInput(t, true, "")
	t.Setenv("GEARS_AGENT", "envy")
	if err := runIn(t, ws, "init"); err != nil {
		t.Fatalf("re-run asked again or failed: %v", err)
	}
	if got := savedName(t, ws); got != "claude-test" {
		t.Fatalf("re-run changed the name to %q", got)
	}

	// --name replaces it.
	if err := runIn(t, ws, "init", "--name", "renamed"); err != nil {
		t.Fatal(err)
	}
	if got := savedName(t, ws); got != "renamed" {
		t.Fatalf("--name did not replace: %q", got)
	}
}

// GRS-63: empty Enter takes the suggested login label.
func TestInitPromptSuggestsLoginLabel(t *testing.T) {
	initEnv(t, true, "\n")
	defer func(f func() string) { session.LoginLabel = f }(session.LoginLabel)
	session.LoginLabel = func() string { return "codex+box" }
	ws := t.TempDir()
	if err := runIn(t, ws, "init"); err != nil {
		t.Fatal(err)
	}
	if got := savedName(t, ws); got != "codex+box" {
		t.Fatalf("saved %q", got)
	}
}

// GRS-63: --name, then -a, then GEARS_AGENT name a fresh workspace without
// asking (stdin is not a terminal).
func TestInitNameFromFlagAgentAndEnv(t *testing.T) {
	initEnv(t, false, "")
	t.Setenv("GEARS_AGENT", "envy")

	ws := t.TempDir()
	if err := runIn(t, ws, "-a", "grok", "init", "--name", "named"); err != nil {
		t.Fatal(err)
	}
	if got := savedName(t, ws); got != "named" {
		t.Fatalf("--name: %q", got)
	}

	resetInitFlags()
	ws = t.TempDir()
	if err := runIn(t, ws, "-a", "grok", "init"); err != nil {
		t.Fatal(err)
	}
	if got := savedName(t, ws); got != "grok" {
		t.Fatalf("-a: %q", got)
	}

	resetInitFlags()
	ws = t.TempDir()
	if err := runIn(t, ws, "init"); err != nil {
		t.Fatal(err)
	}
	if got := savedName(t, ws); got != "envy" {
		t.Fatalf("GEARS_AGENT: %q", got)
	}
}
