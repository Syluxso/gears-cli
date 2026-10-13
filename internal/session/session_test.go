package session

import (
	"strings"
	"testing"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
)

func TestEntryIDsAreUUIDsAndResolve(t *testing.T) {
	res, err := project.Init(t.TempDir(), "sesstest", "SE")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project

	a, err := Append(p, "done", "alice", "first")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Append(p, "next", "bob", "second")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ID) != 36 || a.ID == b.ID {
		t.Fatalf("ids %q %q: want distinct UUIDs", a.ID, b.ID)
	}

	for _, ref := range []string{a.ID, a.Short(), "#" + a.Short(), strings.ToUpper(a.Short())} {
		e, err := Find(p, ref)
		if err != nil || e.ID != a.ID || e.Agent != "alice" {
			t.Fatalf("Find(%q) = %v, %v", ref, e, err)
		}
	}
	// Entries written in the same minute share their leading digits, so a
	// short prefix is ambiguous rather than silently picking one.
	if _, err := Find(p, a.ID[:6]); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("shared prefix: want ambiguous, got %v", err)
	}
	if _, err := Find(p, "abc"); err == nil {
		t.Fatal("three characters should be refused")
	}
	if Exists(p, "ffffffffffff") {
		t.Fatal("unknown id reported as existing")
	}

	got, err := Today(p)
	if err != nil || len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("Today = %v, %v", got, err)
	}
}

func TestEnsureUIDsBackfillsOldRows(t *testing.T) {
	res, err := project.Init(t.TempDir(), "legacy", "LG")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	// One row from before append minted ids, one with an old hex placeholder.
	if _, err := db.Exec(`INSERT INTO session_entries (at, agent, kind, text) VALUES (?, 'a', 'done', 'old')`, store.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session_entries (at, agent, kind, text, uid) VALUES (?, 'a', 'done', 'hex', ?)`, store.Now(), strings.Repeat("ab", 16)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	got, err := Today(p)
	if err != nil || len(got) != 2 {
		t.Fatalf("Today = %v, %v", got, err)
	}
	for _, e := range got {
		if len(e.ID) != 36 {
			t.Fatalf("entry %q kept id %q", e.Text, e.ID)
		}
	}
	again, _ := Today(p)
	if again[0].ID != got[0].ID || again[1].ID != got[1].ID {
		t.Fatal("ids changed on a second read")
	}
}
