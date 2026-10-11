// Package store opens the project's local SQLite cache and owns its schema.
// Every table here mirrors what the API record will hold, so rows written
// offline sync up unchanged once there is somewhere to send them.
package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Syluxso/gears-cli/internal/project"
)

// Open returns the local record for a project, creating it and its tables
// on first use. Callers close it.
func Open(p *project.Project) (*sql.DB, error) {
	if err := os.MkdirAll(p.GearboxPath(), 0o755); err != nil {
		return nil, err
	}
	migrateLegacy(p)
	// busy_timeout: the MCP server dispatches tool calls concurrently and each
	// verb opens its own handle, so a writer must wait for another rather
	// than fail with SQLITE_BUSY. WAL lets readers run during a write.
	dsn := "file:" + filepath.ToSlash(p.LocalPath()) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := addColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// columns added after a table first shipped. CREATE TABLE IF NOT EXISTS
// never adds a column to an existing table, so each one is checked against
// pragma table_info and added when missing. Append here; never edit a line.
var columns = []struct{ table, column, def string }{
	{"card_states", "wip_limit", "INTEGER NOT NULL DEFAULT 0"},
	{"cards", "size", "TEXT NOT NULL DEFAULT ''"},
	{"cards", "assignee", "TEXT NOT NULL DEFAULT ''"},
	{"cards", "parent_id", "TEXT NOT NULL DEFAULT ''"},
	{"cards", "due_at", "TEXT NOT NULL DEFAULT ''"},
	{"cards", "estimate_minutes", "INTEGER NOT NULL DEFAULT 0"},
	// Runbooks v2: a run binds to a card and a branch; steps know when they
	// started, carry an argument, a name and a when: condition.
	{"runs", "card_id", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "branch", "TEXT NOT NULL DEFAULT ''"},
	{"run_steps", "started_at", "TEXT NOT NULL DEFAULT ''"},
	{"run_steps", "arg", "TEXT NOT NULL DEFAULT ''"},
	{"run_steps", "name", "TEXT NOT NULL DEFAULT ''"},
	{"run_steps", "when_cond", "TEXT NOT NULL DEFAULT ''"},
}

func addColumns(db *sql.DB) error {
	for _, c := range columns {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, c.table)
		if err != nil {
			return err
		}
		have := false
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			if name == c.column {
				have = true
			}
		}
		rows.Close()
		if have {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.column + ` ` + c.def); err != nil {
			return err
		}
	}
	return nil
}

// Exists reports whether the local record is present, so read-only commands
// can answer "nothing yet" without creating it.
func Exists(p *project.Project) bool {
	migrateLegacy(p)
	_, err := os.Stat(p.LocalPath())
	return err == nil
}

// migrateLegacy moves a record from where earlier CLIs kept it
// (.gears/local.db, before that .gears/cache.db) into .gearbox/, with its
// WAL sidecars, once. The file was always the record; only the place was wrong.
func migrateLegacy(p *project.Project) {
	if _, err := os.Stat(p.LocalPath()); err == nil {
		return
	}
	for _, old := range p.LegacyLocalPaths() {
		if _, err := os.Stat(old); err != nil {
			continue
		}
		if err := os.MkdirAll(p.GearboxPath(), 0o755); err != nil {
			return
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Rename(old+suffix, p.LocalPath()+suffix)
		}
		return
	}
}

// Now is the single timestamp format for the cache: RFC3339 in UTC, which
// sorts as text.
func Now() string { return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339) }

// Parse turns a stored timestamp back into local time.
func Parse(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.Local()
}

var schema = []string{
	// Sessions: one timeline per project, entries from every agent.
	`CREATE TABLE IF NOT EXISTS session_entries (
		id    INTEGER PRIMARY KEY,
		at    TEXT NOT NULL,
		agent TEXT NOT NULL,
		kind  TEXT NOT NULL,
		text  TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_session_entries_at ON session_entries(at)`,

	// Artifacts: the row is the rule, the file is the data.
	`CREATE TABLE IF NOT EXISTS artifacts (
		id           TEXT PRIMARY KEY,
		prefix       TEXT NOT NULL,
		slug         TEXT NOT NULL,
		file         TEXT NOT NULL UNIQUE,
		title        TEXT NOT NULL DEFAULT '',
		status       TEXT NOT NULL DEFAULT '',
		created_at   TEXT NOT NULL,
		created_by   TEXT NOT NULL,
		updated_at   TEXT NOT NULL,
		body_hash    TEXT NOT NULL DEFAULT '',
		file_present INTEGER NOT NULL DEFAULT 1,
		shape_ok     INTEGER NOT NULL DEFAULT 1,
		shape_errors TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS artifact_tags (
		artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
		tag         TEXT NOT NULL,
		PRIMARY KEY (artifact_id, tag)
	)`,

	// Desk: type is lifecycle. state is open or closed; how it closes depends
	// on type (read, answered, done, dismissed). handoff never closes.
	`CREATE TABLE IF NOT EXISTS desk_items (
		id         TEXT PRIMARY KEY,
		type       TEXT NOT NULL,
		from_agent TEXT NOT NULL,
		to_agent   TEXT NOT NULL DEFAULT '',
		text       TEXT NOT NULL,
		ref        TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		state      TEXT NOT NULL DEFAULT 'open',
		closed_at  TEXT NOT NULL DEFAULT '',
		closed_by  TEXT NOT NULL DEFAULT '',
		answer     TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_desk_items_state ON desk_items(state, created_at)`,

	// Cards: one board per project. States are swimlanes; one default, one
	// terminal. Numbers never reuse (card_sequence), even across soft deletes.
	// Tags, comments, links, checklists and events hang off a card.
	`CREATE TABLE IF NOT EXISTS card_states (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL UNIQUE,
		position    INTEGER NOT NULL,
		is_default  INTEGER NOT NULL DEFAULT 0,
		is_terminal INTEGER NOT NULL DEFAULT 0,
		wip_limit   INTEGER NOT NULL DEFAULT 0,
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS card_sequence (
		id          INTEGER PRIMARY KEY CHECK (id = 1),
		last_number INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS cards (
		id               TEXT PRIMARY KEY,
		number           INTEGER NOT NULL UNIQUE,
		key              TEXT NOT NULL UNIQUE,
		title            TEXT NOT NULL,
		body             TEXT NOT NULL DEFAULT '',
		type             TEXT NOT NULL DEFAULT 'story',
		state_id         TEXT NOT NULL REFERENCES card_states(id),
		size             TEXT NOT NULL DEFAULT '',
		priority         INTEGER NOT NULL DEFAULT 0,
		position         INTEGER NOT NULL DEFAULT 0,
		card_data        TEXT NOT NULL DEFAULT '{}',
		assignee         TEXT NOT NULL DEFAULT '',
		parent_id        TEXT NOT NULL DEFAULT '',
		due_at           TEXT NOT NULL DEFAULT '',
		estimate_minutes INTEGER NOT NULL DEFAULT 0,
		artifact         TEXT NOT NULL DEFAULT '',
		external_key     TEXT NOT NULL DEFAULT '',
		created_by       TEXT NOT NULL,
		created_at       TEXT NOT NULL,
		updated_at       TEXT NOT NULL,
		completed_at     TEXT NOT NULL DEFAULT '',
		deleted_at       TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_cards_state ON cards(state_id, position, number)`,
	`CREATE TABLE IF NOT EXISTS card_tags (
		card_id TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
		tag     TEXT NOT NULL,
		PRIMARY KEY (card_id, tag)
	)`,
	`CREATE TABLE IF NOT EXISTS card_comments (
		id         TEXT PRIMARY KEY,
		card_id    TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
		body       TEXT NOT NULL,
		agent      TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		deleted_at TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_card_comments_card ON card_comments(card_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS card_links (
		id             TEXT PRIMARY KEY,
		card_id        TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
		kind           TEXT NOT NULL,
		target_card_id TEXT NOT NULL DEFAULT '',
		url            TEXT NOT NULL DEFAULT '',
		title          TEXT NOT NULL DEFAULT '',
		agent          TEXT NOT NULL,
		created_at     TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_card_links_target ON card_links(target_card_id)`,
	`CREATE TABLE IF NOT EXISTS card_checklists (
		id         TEXT PRIMARY KEY,
		card_id    TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
		title      TEXT NOT NULL,
		position   INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS card_checklist_items (
		id           TEXT PRIMARY KEY,
		checklist_id TEXT NOT NULL REFERENCES card_checklists(id) ON DELETE CASCADE,
		title        TEXT NOT NULL,
		done         INTEGER NOT NULL DEFAULT 0,
		position     INTEGER NOT NULL DEFAULT 0,
		done_at      TEXT NOT NULL DEFAULT '',
		done_by      TEXT NOT NULL DEFAULT '',
		created_at   TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS card_events (
		id      INTEGER PRIMARY KEY,
		card_id TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
		at      TEXT NOT NULL,
		agent   TEXT NOT NULL,
		kind    TEXT NOT NULL,
		detail  TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_card_events_card ON card_events(card_id, at)`,

	// Runs: one instance of a runbook. Steps are copied in at start so a
	// later edit to the runbook does not rewrite history.
	`CREATE TABLE IF NOT EXISTS runs (
		id         TEXT PRIMARY KEY,
		runbook    TEXT NOT NULL,
		target     TEXT NOT NULL DEFAULT '',
		agent      TEXT NOT NULL,
		started_at TEXT NOT NULL,
		ended_at   TEXT NOT NULL DEFAULT '',
		status     TEXT NOT NULL DEFAULT 'active'
	)`,
	`CREATE TABLE IF NOT EXISTS run_steps (
		run_id   TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		idx      INTEGER NOT NULL,
		kind     TEXT NOT NULL,
		text     TEXT NOT NULL,
		"check"  TEXT NOT NULL DEFAULT '',
		state    TEXT NOT NULL DEFAULT 'pending',
		evidence TEXT NOT NULL DEFAULT '',
		done_at  TEXT NOT NULL DEFAULT '',
		reason   TEXT NOT NULL DEFAULT '',
		desk_item TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, idx)
	)`,
}
