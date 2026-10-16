// Package store opens the project's local SQLite cache and owns its schema.
// Every table here mirrors what the API record will hold, so rows written
// offline sync up unchanged once there is somewhere to send them.
package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Syluxso/gears-cli/internal/project"
)

var schemaMu sync.Mutex

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
	// _txlock=immediate (GRS-51): a transaction takes the write lock when it
	// begins, so busy_timeout covers it. A deferred one that reads and then
	// writes fails at once with "database is locked" when another process
	// (gears run's board stream, a CLI write) committed in between.
	dsn := "file:" + filepath.ToSlash(p.LocalPath()) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// One schema pass at a time in this process: verbs open the cache from
	// several goroutines (status), and two first opens racing on the same
	// ALTER fail with "duplicate column name".
	schemaMu.Lock()
	defer schemaMu.Unlock()
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, err
		}
	}
	added, err := addColumns(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	for col := range added {
		for _, q := range backfill[col] {
			if _, err := db.Exec(q); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	for _, s := range after {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, err
		}
	}
	if added["card_events.uid"] {
		if err := EnsureEventUIDs(db); err != nil {
			db.Close()
			return nil, err
		}
	}
	// GRS-40: artifacts carry their project. A standalone project binding
	// (a clone bound by remote) pushed every artifact under its own id
	// before the column existed, so its rows take that id; at a workspace
	// root they stay workspace-level (empty).
	if added["artifacts.project_id"] && !p.IsWorkspace() {
		if _, err := db.Exec(`UPDATE artifacts SET project_id = ? WHERE project_id = ''`, p.ID); err != nil {
			db.Close()
			return nil, err
		}
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
	// Sync (slice 1): session entries get a client-minted uuid, desk items an
	// updated_at, artifacts the byz-files id their body lives under.
	{"session_entries", "uid", "TEXT NOT NULL DEFAULT ''"},
	{"session_entries", "project_id", "TEXT NOT NULL DEFAULT ''"},
	{"desk_items", "updated_at", "TEXT NOT NULL DEFAULT ''"},
	{"artifacts", "remote_file_id", "TEXT NOT NULL DEFAULT ''"},
	// GRS-40: one cache per workspace holds every project's artifacts; the
	// project is a tag on the row, empty for workspace-level ones.
	{"artifacts", "project_id", "TEXT NOT NULL DEFAULT ''"},
	// GRS-47: cards sync. Children get updated_at for last-write-wins (filled
	// from created_at when added), cards the label of their last writer, and
	// activity lines a client-minted UUID v7 (the integer id stays local).
	{"card_links", "updated_at", "TEXT NOT NULL DEFAULT ''"},
	{"card_checklists", "updated_at", "TEXT NOT NULL DEFAULT ''"},
	{"card_checklist_items", "updated_at", "TEXT NOT NULL DEFAULT ''"},
	{"cards", "updated_by", "TEXT NOT NULL DEFAULT ''"},
	{"card_events", "uid", "TEXT NOT NULL DEFAULT ''"},
	// GRS-50: cards close. closed_at is when a card left the board ("" is
	// open); state_changed_at is when it entered its current state, which
	// the API's 7-day auto-close of terminal cards reads.
	{"cards", "closed_at", "TEXT NOT NULL DEFAULT ''"},
	{"cards", "state_changed_at", "TEXT NOT NULL DEFAULT ''"},
	// GRS-20: content_hash is the hash (artifact.Hash) of the body the API
	// last took or gave for this artifact; a file whose hash differs is
	// uploaded on every sync until one succeeds.
	{"artifacts", "content_hash", "TEXT NOT NULL DEFAULT ''"},
	// GRS-35: author is the user (account id) who created the item, set by
	// the API and pulled back; assigned_to the user it is for ("" nobody).
	{"desk_items", "author", "TEXT NOT NULL DEFAULT ''"},
	{"desk_items", "assigned_to", "TEXT NOT NULL DEFAULT ''"},
	// GRS-69: a canceled record run's reason, who and via what.
	{"record_runs", "cancel_reason", "TEXT NOT NULL DEFAULT ''"},
	{"record_runs", "canceled_by", "TEXT NOT NULL DEFAULT ''"},
	{"record_runs", "canceled_label", "TEXT NOT NULL DEFAULT ''"},
	{"record_runs", "canceled_via", "TEXT NOT NULL DEFAULT ''"},
	{"record_runs", "canceled_at", "TEXT NOT NULL DEFAULT ''"},
}

// backfill runs once, right after its column is added: rows from before the
// column take a value derived from what they already hold.
var backfill = map[string][]string{
	"card_links.updated_at":           {`UPDATE card_links SET updated_at = created_at WHERE updated_at = ''`},
	"card_checklists.updated_at":      {`UPDATE card_checklists SET updated_at = created_at WHERE updated_at = ''`},
	"card_checklist_items.updated_at": {`UPDATE card_checklist_items SET updated_at = CASE WHEN done_at > created_at THEN done_at ELSE created_at END WHERE updated_at = ''`},
	"cards.updated_by":                {`UPDATE cards SET updated_by = created_by WHERE updated_by = ''`},
	// The best a row from before GRS-50 can say: its last write.
	"cards.state_changed_at": {`UPDATE cards SET state_changed_at = updated_at WHERE state_changed_at = ''`},
	// An artifact with a file id had its body uploaded: its row hash.
	"artifacts.content_hash": {`UPDATE artifacts SET content_hash = body_hash WHERE remote_file_id <> ''`},
}

// after the columns: indexes on added columns.
var after = []string{
	`CREATE INDEX IF NOT EXISTS idx_card_events_uid ON card_events(uid)`,
}

// EnsureEventUIDs gives every activity line without one a UUID v7, the id
// the API knows it by. Lines written since GRS-47 get one at insert.
func EnsureEventUIDs(db *sql.DB) error {
	rows, err := db.Query(`SELECT id FROM card_events WHERE uid = ''`)
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
		if _, err := db.Exec(`UPDATE card_events SET uid = ? WHERE id = ?`, project.NewID(), id); err != nil {
			return err
		}
	}
	return nil
}

// addColumns returns the "table.column" names it added this time.
func addColumns(db *sql.DB) (map[string]bool, error) {
	added := map[string]bool{}
	for _, c := range columns {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, c.table)
		if err != nil {
			return nil, err
		}
		have := false
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return nil, err
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
			return nil, err
		}
		added[c.table+"."+c.column] = true
	}
	return added, nil
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
	`CREATE TABLE IF NOT EXISTS sync_state (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT ''
	)`,
	// Tombstones: a row deleted here is gone from its table at once; its id
	// waits here until a sync push lands the delete on the server.
	`CREATE TABLE IF NOT EXISTS sync_deletes (
		tbl        TEXT NOT NULL,
		id         TEXT NOT NULL,
		deleted_at TEXT NOT NULL,
		PRIMARY KEY (tbl, id)
	)`,
	// Refused rows (GRS-47): a row the server answered with an error stays
	// here and goes up on every push, whatever the watermark, until the
	// server takes it. The watermark moves on for everything else.
	`CREATE TABLE IF NOT EXISTS sync_retry (
		tbl   TEXT NOT NULL,
		id    TEXT NOT NULL,
		error TEXT NOT NULL DEFAULT '',
		at    TEXT NOT NULL,
		PRIMARY KEY (tbl, id)
	)`,
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

	// Record runs (GRS-58): the cache of gears-api's runs, the ones
	// byz-orchestrator drives. Pulled by sync (table "runs", steps inside
	// the row) and written by the runbook tools from the API's answers. A
	// row is replaced only by a higher version. The runs / run_steps tables
	// above belong to the superseded local engine.
	`CREATE TABLE IF NOT EXISTS record_runs (
		id           TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL DEFAULT '',
		runbook      TEXT NOT NULL,
		status       TEXT NOT NULL,
		current_step TEXT NOT NULL DEFAULT '',
		card_key     TEXT NOT NULL DEFAULT '',
		agent_label  TEXT NOT NULL DEFAULT '',
		started_by   TEXT NOT NULL DEFAULT '',
		version      INTEGER NOT NULL,
		started_at   TEXT NOT NULL,
		finished_at  TEXT NOT NULL DEFAULT '',
		updated_at   TEXT NOT NULL,
		runbook_ref  TEXT NOT NULL DEFAULT '',
		runbook_version INTEGER NOT NULL DEFAULT 0,
		internal     INTEGER NOT NULL DEFAULT 0,
		started_via  TEXT NOT NULL DEFAULT ''
	)`,
	// Adopted runbooks (GRS-59): the workspace's copies of official
	// runbooks as gears-api holds them. Pulled by sync (table "runbooks")
	// and written by runbook_adopt / runbook_edit from the API's answers.
	// row is the copy as JSON; frozen ones refuse the authoring tools.
	`CREATE TABLE IF NOT EXISTS record_runbooks (
		id           TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL DEFAULT '',
		slug         TEXT NOT NULL,
		source_ref   TEXT NOT NULL DEFAULT '',
		frozen       INTEGER NOT NULL DEFAULT 0,
		updated_at   TEXT NOT NULL DEFAULT '',
		row          TEXT NOT NULL
	)`,
	// Run timelines (GRS-72): gears-api's run_events, pulled inside the run
	// rows and written by runbook_history. Append-only; a repeat is ignored.
	`CREATE TABLE IF NOT EXISTS record_run_events (
		run_id   TEXT NOT NULL,
		version  INTEGER NOT NULL,
		type     TEXT NOT NULL,
		step_id  TEXT NOT NULL DEFAULT '',
		at       TEXT NOT NULL DEFAULT '',
		actor    TEXT NOT NULL DEFAULT '',
		label    TEXT NOT NULL DEFAULT '',
		via      TEXT NOT NULL DEFAULT '',
		evidence TEXT NOT NULL DEFAULT '[]',
		reason   TEXT NOT NULL DEFAULT '',
		note     TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, version, type)
	)`,
	`CREATE TABLE IF NOT EXISTS record_run_steps (
		run_id      TEXT NOT NULL REFERENCES record_runs(id) ON DELETE CASCADE,
		step_id     TEXT NOT NULL,
		position    INTEGER NOT NULL,
		kind        TEXT NOT NULL DEFAULT '',
		title       TEXT NOT NULL DEFAULT '',
		status      TEXT NOT NULL,
		evidence    TEXT NOT NULL DEFAULT '[]',
		note        TEXT NOT NULL DEFAULT '',
		skip_reason TEXT NOT NULL DEFAULT '',
		done_by     TEXT NOT NULL DEFAULT '',
		done_label  TEXT NOT NULL DEFAULT '',
		started_at  TEXT NOT NULL DEFAULT '',
		finished_at TEXT NOT NULL DEFAULT '',
		done_via    TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, step_id)
	)`,
}
