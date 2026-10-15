package project

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GRS-33: outside any workspace, Find says so plainly and names the fix.
func TestFindNotAWorkspace(t *testing.T) {
	_, err := Find(t.TempDir())
	if !errors.Is(err, ErrNotBound) {
		t.Fatalf("want ErrNotBound, got %v", err)
	}
	for _, want := range []string{"not a Gears workspace", "no .gears here or above", "gears init at your workspace root"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err, want)
		}
	}
}

// GRS-33: a nested .gears (made before GRS-40, or on another machine) never
// wins over the workspace root above it; Find names the stray once.
func TestFindPrefersOutermost(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, "ws", "WS"); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(root, "projects", "app")
	if err := os.MkdirAll(filepath.Join(stray, Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, Dir, BindingFile), []byte("id: stray-id\nslug: app\nprefix: APP\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(stray, "src", "x")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	old := Warnings
	Warnings = &buf
	defer func() { Warnings = old }()

	for _, dir := range []string{deep, stray, root} {
		p, err := Find(dir)
		if err != nil {
			t.Fatal(err)
		}
		if p.Root != root || p.Slug != "ws" {
			t.Fatalf("Find(%s) = %s (%s), want the root %s", dir, p.Root, p.Slug, root)
		}
	}
	got := buf.String()
	if strings.Count(got, "warning:") != 1 || !strings.Contains(got, filepath.Join(stray, Dir)) || !strings.Contains(got, root) {
		t.Fatalf("want one warning naming %s, got %q", filepath.Join(stray, Dir), got)
	}

	// init in the stray directory is refused and points at the root.
	_, err := Init(stray, "", "")
	var under *UnderBindingError
	if !errors.As(err, &under) || under.Root != root {
		t.Fatalf("init in stray: want refusal pointing at %s, got %v", root, err)
	}
}

// GRS-33: init with no .gears above creates the workspace; init anywhere
// below it, at any depth, is refused, names the root and creates nothing.
func TestInitRootRules(t *testing.T) {
	root := t.TempDir()
	res, err := Init(root, "ws", "WS")
	if err != nil {
		t.Fatalf("init with nothing above must create the workspace: %v", err)
	}
	if res.Project.Root != root || !res.Project.IsWorkspace() {
		t.Fatalf("workspace %+v", res.Project)
	}
	for _, sub := range []string{"docs", filepath.Join("projects", "app", "src")} {
		dir := filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := Init(dir, "", "")
		var under *UnderBindingError
		if !errors.As(err, &under) || under.Root != root || !strings.Contains(err.Error(), root) {
			t.Fatalf("init in %s: want refusal naming %s, got %v", sub, root, err)
		}
		if _, err := os.Stat(filepath.Join(dir, Dir)); !os.IsNotExist(err) {
			t.Fatalf("nested .gears created in %s", sub)
		}
	}
}
