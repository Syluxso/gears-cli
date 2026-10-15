package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Syluxso/gears-cli/internal/project"
)

// runIn executes the root command with args from dir and returns its error.
func runIn(t *testing.T, dir string, args ...string) error {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	root.SetArgs(args)
	return root.Execute()
}

// GRS-33: every command run anywhere under a workspace walks up to the root
// binding; outside any workspace it says plainly that this is not one.
func TestCommandsWalkUpToRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GEARS_API", "http://127.0.0.1:1")
	t.Setenv("GEARS_AGENT", "tester")

	ws := t.TempDir()
	if _, err := project.Init(ws, "ws", "WS"); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(ws, "projects", "app", "src")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	for _, args := range [][]string{
		{"status", "--json"},
		{"desk", "list", "--json"},
		{"card", "list", "--json"},
		{"artifact", "list", "--json"},
		{"session", "list", "--json"},
		{"sync", "--json"},
	} {
		err := runIn(t, deep, args...)
		if errors.Is(err, project.ErrNotBound) {
			t.Errorf("%v in a subfolder did not find the root: %v", args, err)
		}
		if err := runIn(t, outside, args...); !errors.Is(err, project.ErrNotBound) {
			t.Errorf("%v outside a workspace: want ErrNotBound, got %v", args, err)
		}
	}
	// sync gets past the binding to the login check, from the subfolder.
	if err := runIn(t, deep, "sync"); err == nil || errors.Is(err, project.ErrNotBound) {
		t.Errorf("sync from a subfolder: want a login error, got %v", err)
	}
	// run --dir resolves through the same walk.
	c, err := ctxAt(deep)
	if err != nil || c.Project.Root != ws {
		t.Fatalf("run --dir %s: root %v, err %v", deep, c.Project, err)
	}
	if _, err := os.Stat(filepath.Join(deep, project.Dir)); !os.IsNotExist(err) {
		t.Fatal("a command created a nested .gears")
	}
	// init inside the workspace is refused and names the root.
	err = runIn(t, deep, "init")
	var under *project.UnderBindingError
	if !errors.As(err, &under) || under.Root != ws {
		t.Fatalf("init in a subfolder: %v", err)
	}
}
