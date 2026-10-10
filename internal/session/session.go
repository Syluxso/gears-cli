// Package session is the local half of the session log: timestamped entries
// in the project's SQLite cache. When the API exists these rows sync up and
// other agents' rows sync down; the table shape is already what the record
// will hold, so nothing here is thrown away.
package session

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Syluxso/gears/internal/project"
	"github.com/Syluxso/gears/internal/store"
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
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	at := store.Now()
	res, err := db.Exec(
		`INSERT INTO session_entries (at, agent, kind, text) VALUES (?, ?, ?, ?)`,
		at, agent, kind, text,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Entry{ID: id, At: store.Parse(at), Agent: agent, Kind: kind, Text: text}, nil
}

// Exists reports whether an entry id is in the log. Runs use it to verify
// "log a session entry" evidence.
func Exists(p *project.Project, id int64) bool {
	if !store.Exists(p) {
		return false
	}
	db, err := store.Open(p)
	if err != nil {
		return false
	}
	defer db.Close()
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM session_entries WHERE id = ?`, id).Scan(&n)
	return n == 1
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
	if !store.Exists(p) {
		return nil, nil
	}
	db, err := store.Open(p)
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
		e.At = store.Parse(at)
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
