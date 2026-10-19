package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Syluxso/gears-cli/internal/hooks"
)

// GRS-93: gears init --stop-hook and gears hooks install / status /
// uninstall, in temp dirs only.
func TestInitStopHookAndHooksCommands(t *testing.T) {
	initEnv(t, false, "")
	ws := t.TempDir()
	settings := filepath.Join(ws, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"permissions": {"allow": ["Bash(ls)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Plain init writes no hook.
	if err := runIn(t, ws, "init", "--name", "claude-local", "--offline"); err != nil {
		t.Fatal(err)
	}
	if st, _ := hooks.Read(ws); st.Installed {
		t.Fatal("plain init installed the hook")
	}
	resetInitFlags()
	if err := runIn(t, ws, "init", "--stop-hook", "--offline"); err != nil {
		t.Fatal(err)
	}
	st, _ := hooks.Read(ws)
	b, _ := os.ReadFile(settings)
	if !st.Installed || st.Command != "gears runbook next --hook" || !strings.Contains(string(b), `"Bash(ls)"`) {
		t.Fatalf("init --stop-hook: %+v\n%s", st, b)
	}

	// hooks install from a subdirectory finds the root; -a bakes the label.
	sub := filepath.Join(ws, "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	resetInitFlags()
	if err := runIn(t, sub, "-a", "claude-local", "hooks", "install"); err != nil {
		t.Fatal(err)
	}
	if st, _ := hooks.Read(ws); st.Command != "gears -a claude-local runbook next --hook" {
		t.Fatalf("install -a: %+v", st)
	}
	resetInitFlags()
	if err := runIn(t, sub, "hooks", "status"); err != nil {
		t.Fatal(err)
	}
	if err := runIn(t, ws, "hooks", "uninstall"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(settings)
	if strings.Contains(string(b), hooks.Marker) || !strings.Contains(string(b), `"Bash(ls)"`) {
		t.Fatalf("uninstall:\n%s", b)
	}

	// --dir: any directory, workspace or not.
	other := t.TempDir()
	if err := runIn(t, other, "hooks", "install", "--dir", other); err != nil {
		t.Fatal(err)
	}
	if st, _ := hooks.Read(other); !st.Installed {
		t.Fatal("--dir install")
	}
	resetInitFlags()
	if err := runIn(t, t.TempDir(), "hooks", "install"); err == nil || !strings.Contains(err.Error(), "--dir") {
		t.Fatalf("outside a workspace: %v", err)
	}
}
