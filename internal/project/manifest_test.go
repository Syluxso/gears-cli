package project

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitRepo(t *testing.T, dir, remote string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	if remote != "" {
		run("remote", "add", "origin", remote)
	}
}

// GRS-40: a scan records every git repo under projects/ (with or without a
// remote), reuses the id the API holds for a remote, mints one offline,
// marks a vanished repo missing without dropping it, and keeps ids stable.
func TestScan(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	res, err := Init(root, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	gitRepo(t, filepath.Join(root, "projects", "known"), "git@github.com:example/known.git")
	gitRepo(t, filepath.Join(root, "projects", "bare"), "")                              // no commits, no remote
	if err := os.MkdirAll(filepath.Join(root, "projects", "notes"), 0o755); err != nil { // not a repo
		t.Fatal(err)
	}
	const apiID = "01a12688-563c-7dda-a360-db460f196103"
	asked := 0
	lookup := func(remote string) (string, bool) {
		asked++
		if remote == "git@github.com:example/known.git" {
			return apiID, true
		}
		return "", false
	}

	r, err := Scan(p, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Added) != 2 || len(r.Manifest.Projects) != 2 {
		t.Fatalf("added %v manifest %+v", r.Added, r.Manifest.Projects)
	}
	m, _ := LoadManifest(p)
	known, bare := m.Lookup("known"), m.Lookup("projects/bare")
	if known == nil || known.ID != apiID || known.Path != "projects/known" || known.Remote != "git@github.com:example/known.git" {
		t.Fatalf("known: %+v", known)
	}
	if bare == nil || bare.Remote != "" || len(bare.ID) != 36 || bare.ID == apiID {
		t.Fatalf("bare: %+v", bare)
	}
	if asked != 1 {
		t.Fatalf("lookups: %d (a repo with no remote is not looked up)", asked)
	}
	bareID := bare.ID

	// Re-scan: nothing changes, ids stay.
	r, err = Scan(p, lookup)
	if err != nil || r.Changed() {
		t.Fatalf("rescan changed: %+v %v", r, err)
	}

	// The repo goes: marked missing, never dropped.
	if err := os.RemoveAll(filepath.Join(root, "projects", "bare")); err != nil {
		t.Fatal(err)
	}
	r, err = Scan(p, nil)
	if err != nil || len(r.Missing) != 1 || r.Missing[0] != "projects/bare" {
		t.Fatalf("missing: %+v %v", r, err)
	}
	m, _ = LoadManifest(p)
	if e := m.Lookup("bare"); e == nil || !e.Missing || e.ID != bareID {
		t.Fatalf("bare after removal: %+v", e)
	}
	// It comes back: same id, no longer missing.
	gitRepo(t, filepath.Join(root, "projects", "bare"), "")
	r, err = Scan(p, nil)
	if err != nil || len(r.Returned) != 1 {
		t.Fatalf("returned: %+v %v", r, err)
	}
	m, _ = LoadManifest(p)
	if e := m.Lookup("bare"); e == nil || e.Missing || e.ID != bareID {
		t.Fatalf("bare back: %+v", e)
	}

	// A repo moves directory (offline): the new path takes the missing
	// entry's id instead of minting one.
	if err := os.Rename(filepath.Join(root, "projects", "known"), filepath.Join(root, "projects", "known2")); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(p, nil); err != nil {
		t.Fatal(err)
	}
	m, _ = LoadManifest(p)
	if e := m.Lookup("projects/known2"); e == nil || e.ID != apiID {
		t.Fatalf("moved: %+v", m.Projects)
	}
	if len(m.Projects) != 2 {
		t.Fatalf("moved repo left a duplicate: %+v", m.Projects)
	}

	// Offline, a new repo gets a fresh id; online later, the API's id for
	// its remote is adopted.
	gitRepo(t, filepath.Join(root, "projects", "late"), "https://github.com/example/late.git")
	if _, err := Scan(p, nil); err != nil {
		t.Fatal(err)
	}
	m, _ = LoadManifest(p)
	minted := m.Lookup("late").ID
	r, err = Scan(p, func(remote string) (string, bool) {
		if remote == "https://github.com/example/late.git" {
			return "01a1aaaa-0000-7000-8000-000000000001", true
		}
		return "", false
	})
	if err != nil || len(r.Adopted) != 1 {
		t.Fatalf("adopt: %+v %v", r, err)
	}
	m, _ = LoadManifest(p)
	if got := m.Lookup("late").ID; got == minted || got != "01a1aaaa-0000-7000-8000-000000000001" {
		t.Fatalf("late id %s (minted %s)", got, minted)
	}
}

// GRS-40: init inside a directory under a workspace binding is refused and
// creates nothing; a standalone directory still binds.
func TestInitRefusesUnderWorkspace(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, "ws", "WS"); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "projects", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Init(sub, "", "")
	var under *UnderBindingError
	if !errors.As(err, &under) || under.Root != root || !under.Workspace {
		t.Fatalf("want refusal pointing at %s, got %v", root, err)
	}
	if _, err := os.Stat(filepath.Join(sub, Dir)); !os.IsNotExist(err) {
		t.Fatalf("nested .gears created: %v", err)
	}
	// Re-running init at the root itself is fine.
	if _, err := Init(root, "", ""); err != nil {
		t.Fatal(err)
	}
	// A standalone clone with no workspace above it binds as before.
	if _, err := Init(t.TempDir(), "clone", ""); err != nil {
		t.Fatal(err)
	}
}

// Current is the manifest entry whose path holds the directory.
func TestCurrent(t *testing.T) {
	root := t.TempDir()
	res, err := Init(root, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	m := &Manifest{Projects: []ManifestEntry{{ID: "id-app", Slug: "app", Path: "projects/app"}, {ID: "id-apx", Slug: "app-x", Path: "projects/app-x"}}}
	if err := m.Save(p); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{
		root:                                   "",
		filepath.Join(root, "projects"):        "",
		filepath.Join(root, "projects", "app"): "id-app",
		filepath.Join(root, "projects", "app", "src", "x"): "id-app",
		filepath.Join(root, "projects", "app-x"):           "id-apx",
	} {
		got := ""
		if e := Current(p, dir); e != nil {
			got = e.ID
		}
		if got != want {
			t.Errorf("Current(%s) = %q, want %q", dir, got, want)
		}
	}
}
