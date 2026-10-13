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

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
)

// Kinds an entry may have. The kind is the whole taxonomy; there is no
// free-form tag field, so the log stays scannable.
var Kinds = []string{"done", "decision", "problem", "next"}

// Entry is one line of the log. ID is a UUID v7 minted when the entry is
// written; it is the same on every machine the entry syncs to. The SQLite
// rowid is private to one cache and never leaves this package.
type Entry struct {
	ID    string
	At    time.Time
	Agent string
	Kind  string
	Text  string
}

// Short is the id shown in listings and given as runbook evidence: the last
// eight hex digits. A v7 id's leading digits are its timestamp, so entries
// written in the same minute share a prefix; the tail is random.
func (e Entry) Short() string {
	if len(e.ID) < 8 {
		return e.ID
	}
	return e.ID[len(e.ID)-8:]
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
	id := project.NewID()
	if _, err := db.Exec(
		`INSERT INTO session_entries (at, agent, kind, text, uid) VALUES (?, ?, ?, ?, ?)`,
		at, agent, kind, text, id,
	); err != nil {
		return nil, err
	}
	return &Entry{ID: id, At: store.Parse(at), Agent: agent, Kind: kind, Text: text}, nil
}

// EnsureUIDs gives every entry written before ids were minted at append time
// its v7 id, and replaces the hex placeholders an older sync backfilled.
// Idempotent; cheap when there is nothing to fix.
func EnsureUIDs(db *sql.DB) error {
	rows, err := db.Query(`SELECT id FROM session_entries WHERE uid = '' OR length(uid) = 32`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := db.Exec(`UPDATE session_entries SET uid = ? WHERE id = ?`, project.NewID(), id); err != nil {
			return err
		}
	}
	return nil
}

// Exists reports whether an entry is in the log. Runs use it to verify
// "log a session entry" evidence.
func Exists(p *project.Project, ref string) bool {
	_, err := Find(p, ref)
	return err == nil
}

// Find returns one entry by full id, or by a unique leading or trailing run
// of at least six characters (listings show the last eight), so a run can
// check who wrote it and when. A leading # is ignored.
func Find(p *project.Project, ref string) (*Entry, error) {
	ref = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ref), "#"))
	if len(ref) < 6 {
		return nil, fmt.Errorf("session entry %q: give at least six characters of its id", ref)
	}
	if !store.Exists(p) {
		return nil, fmt.Errorf("session entry %q not found", ref)
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := EnsureUIDs(db); err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT uid, at, agent, kind, text FROM session_entries WHERE uid = ? OR uid LIKE ? OR uid LIKE ?`,
		ref, ref+"%", "%"+ref,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []Entry
	for rows.Next() {
		var e Entry
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Agent, &e.Kind, &e.Text); err != nil {
			return nil, err
		}
		e.At = store.Parse(at)
		hits = append(hits, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("session entry %q not found", ref)
	case 1:
		return &hits[0], nil
	}
	return nil, fmt.Errorf("session entry %q is ambiguous; give more of its id", ref)
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
	if err := EnsureUIDs(db); err != nil {
		return nil, err
	}

	rows, err := db.Query(
		`SELECT uid, at, agent, kind, text FROM session_entries WHERE at >= ? ORDER BY at, id`,
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
