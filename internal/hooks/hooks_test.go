package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GRS-93: the Stop hook in .claude/settings.json, merged in temp dirs only.

func readFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallFresh(t *testing.T) {
	dir := t.TempDir()
	st, err := Install(dir, Command(""))
	if err != nil || !st.Changed || !st.Installed {
		t.Fatalf("install: %+v %v", st, err)
	}
	want := `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "gears runbook next --hook",
            "timeout": 10
          }
        ]
      }
    ]
  }
}
`
	if got := readFile(t, dir); got != want {
		t.Fatalf("fresh file:\n%s", got)
	}
	// Idempotent: nothing changes, the file is byte for byte the same.
	st, err = Install(dir, Command(""))
	if err != nil || st.Changed || readFile(t, dir) != want {
		t.Fatalf("again: %+v %v", st, err)
	}
	if st, _ := Read(dir); !st.Installed || st.Command != "gears runbook next --hook" {
		t.Fatalf("read: %+v", st)
	}
	// Uninstall of a file holding only the hook removes the file.
	st, err = Uninstall(dir)
	if err != nil || !st.Removed {
		t.Fatalf("uninstall: %+v %v", st, err)
	}
	if _, err := os.Stat(Path(dir)); !os.IsNotExist(err) {
		t.Fatalf("file left: %v", err)
	}
	if st, err := Uninstall(dir); err != nil || st.Removed {
		t.Fatalf("uninstall again: %+v %v", st, err)
	}
}

func TestInstallMergesExisting(t *testing.T) {
	dir := t.TempDir()
	existing := `{
    "permissions": {"allow": ["Bash(go test:*)"], "deny": []},
    "env": {"FOO": "a<b&c", "N": 1.50},
    "hooks": {
        "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo pre"}]}],
        "Stop": [{"hooks": [{"type": "command", "command": "notify-send done"}]}]
    },
    "model": "opus"
}`
	if err := os.MkdirAll(filepath.Dir(Path(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(dir), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, Command("claude-local")); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, dir)
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, got)
	}
	// Order, other keys, other hooks and scalar spelling are kept.
	order := []string{`"permissions"`, `"env"`, `"hooks"`, `"model"`}
	last := -1
	for _, k := range order {
		i := strings.Index(got, k)
		if i < last {
			t.Fatalf("key order lost:\n%s", got)
		}
		last = i
	}
	for _, want := range []string{`"FOO": "a<b&c"`, `"N": 1.50`, `"command": "echo pre"`, `"command": "notify-send done"`,
		`"command": "gears -a claude-local runbook next --hook"`, "\n  \"model\": \"opus\"\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s:\n%s", want, got)
		}
	}
	stop := v["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 2 {
		t.Fatalf("Stop groups: %v", stop)
	}
	// A different agent updates the command in place, not a second hook.
	st, err := Install(dir, Command("grok"))
	if err != nil || !st.Changed {
		t.Fatalf("update: %+v %v", st, err)
	}
	got = readFile(t, dir)
	if strings.Count(got, Marker) != 1 || !strings.Contains(got, "gears -a grok runbook next --hook") {
		t.Fatalf("update:\n%s", got)
	}
	// Uninstall leaves everything else.
	if st, err := Uninstall(dir); err != nil || !st.Removed {
		t.Fatalf("uninstall: %+v %v", st, err)
	}
	got = readFile(t, dir)
	if strings.Contains(got, Marker) || !strings.Contains(got, "notify-send done") || !strings.Contains(got, "echo pre") ||
		!strings.Contains(got, `"model": "opus"`) || !strings.Contains(got, `"allow": [`) {
		t.Fatalf("after uninstall:\n%s", got)
	}
	if st, _ := Read(dir); st.Installed {
		t.Fatalf("still installed: %+v", st)
	}
}

func TestUninstallDropsEmptyHooks(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(Path(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(dir), []byte(`{"model": "opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, Command("")); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(dir); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir); got != "{\n  \"model\": \"opus\"\n}\n" {
		t.Fatalf("after:\n%s", got)
	}
}

func TestBadSettingsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(Path(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"model": `, `[1]`, `{"hooks": []}`, `{"hooks": {"Stop": {}}}`} {
		if err := os.WriteFile(Path(dir), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Install(dir, Command("")); err == nil {
			t.Fatalf("install over %s accepted", bad)
		}
		if got := readFile(t, dir); got != bad {
			t.Fatalf("file changed: %s", got)
		}
	}
}
