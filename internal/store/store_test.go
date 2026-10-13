package store

import (
	"database/sql"
	"os"
	"testing"

	"github.com/Syluxso/gears-cli/internal/project"
)

// GRS-47: a cache from before cards sync gets the new columns, its rows
// filled from what they hold, and every activity line a UUID v7.
func TestOpenMigratesCardTablesForSync(t *testing.T) {
	res, err := project.Init(t.TempDir(), "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	if err := os.MkdirAll(p.GearboxPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", "file:"+p.LocalPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE card_events (id INTEGER PRIMARY KEY, card_id TEXT NOT NULL, at TEXT NOT NULL, agent TEXT NOT NULL, kind TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE card_links (id TEXT PRIMARY KEY, card_id TEXT NOT NULL, kind TEXT NOT NULL, target_card_id TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', agent TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`INSERT INTO card_events (card_id, at, agent, kind, detail) VALUES ('c1', '2026-10-01T00:00:00Z', 'a', 'created', ''), ('c1', '2026-10-01T00:00:01Z', 'a', 'moved', '')`,
		`INSERT INTO card_links (id, card_id, kind, target_card_id, agent, created_at) VALUES ('l1', 'c1', 'related', 'c2', 'a', '2026-10-01T00:00:00Z')`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT uid FROM card_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			t.Fatal(err)
		}
		if len(uid) != 36 || uid[14] != '7' || seen[uid] {
			t.Fatalf("event uid %q", uid)
		}
		seen[uid] = true
	}
	rows.Close()
	if len(seen) != 2 {
		t.Fatalf("uids: %v", seen)
	}
	var upd string
	if err := db.QueryRow(`SELECT updated_at FROM card_links WHERE id = 'l1'`).Scan(&upd); err != nil || upd != "2026-10-01T00:00:00Z" {
		t.Fatalf("link updated_at %q %v", upd, err)
	}
}
