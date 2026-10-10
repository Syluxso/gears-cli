// Package session is the local half of the session log: timestamped entries
// in the project's SQLite cache. When the API exists these rows sync up and
// other agents' rows sync down; the table shape is already what the record
// will hold, so nothing here is thrown away.
package session

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Syluxso/gears/internal/project"
)

// Kinds an entry may have. The kind is the whole taxonomy; there is no
// free-form tag field, so the log stays scannable.
var Kinds = []string{"done", "decision", "problem", "next"}

// Entry is one line of the log.
type Entry struct {
	ID    int64
	At    time.Time
	Agent string
	Kind  string
	Text  string
}

func open(p *project.Project) (*sql.DB, error) {
	if err := os.MkdirAll(p.GearsDir(), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", p.CachePath())
	if err != nil {
		return nil, err
	}
	// One writer at a time is fine for a local cache; WAL lets a reader
	// (gears status) run while a writer (gears session new) is mid-commit.
	stmts := []string{
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE IF NOT EXISTS session_entries (
			id    INTEGER PRIMARY KEY,
			at    TEXT NOT NULL,
			agent TEXT NOT NULL,
			kind  TEXT NOT NULL,
			text  TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_entries_at ON session_entries(at)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// DefaultAgent is the label used when none is given: $GEARS_AGENT, else
// "local". Real agent identity arrives with gears login.
func DefaultAgent() string {
	if a := strings.TrimSpace(os.Getenv("GEARS_AGENT")); a != "" {
		return a
	}
	return "local"
}

// Append writes one entry and returns it with its timestamp.
func Append(p *project.Project, kind, agent, text string) (*Entry, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if !validKind(kind) {
		return nil, fmt.Errorf("kind %q; one of %s", kind, strings.Join(Kinds, ", "))
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("entry text is empty")
	}
	if agent == "" {
		agent = DefaultAgent()
	}
	db, err := open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	e := &Entry{At: time.Now().UTC().Truncate(time.Second), Agent: agent, Kind: kind, Text: text}
	res, err := db.Exec(
		`INSERT INTO session_entries (at, agent, kind, text) VALUES (?, ?, ?, ?)`,
		e.At.Format(time.RFC3339), e.Agent, e.Kind, e.Text,
	)
	if err != nil {
		return nil, err
	}
	e.ID, _ = res.LastInsertId()
	e.At = e.At.Local()
	return e, nil
}

// Today returns today's entries (local calendar day), oldest first.
func Today(p *project.Project) ([]Entry, error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return since(p, start)
}

// Recent returns entries from the last n calendar days, oldest first.
func Recent(p *project.Project, days int) ([]Entry, error) {
	if days < 1 {
		days = 1
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
	return since(p, start)
}

func since(p *project.Project, start time.Time) ([]Entry, error) {
	if _, err := os.Stat(p.CachePath()); err != nil {
		return nil, nil // no cache yet means no entries, not an error
	}
	db, err := open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(
		`SELECT id, at, agent, kind, text FROM session_entries WHERE at >= ? ORDER BY at, id`,
		start.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Agent, &e.Kind, &e.Text); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return nil, err
		}
		e.At = t.Local()
		out = append(out, e)
	}
	return out, rows.Err()
}

func validKind(k string) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}
