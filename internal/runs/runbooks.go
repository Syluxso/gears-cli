package runs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
)

// Adopted runbooks (GRS-59): the cache of a workspace's copies of official
// runbooks as gears-api holds them. gears-api is the only writer; this is
// what sync pulled and what runbook_adopt / runbook_edit last got back.

// SaveRunbookDB caches a copy unless the cached one is newer.
func SaveRunbookDB(db *sql.DB, rb api.AdoptedRunbook) (bool, error) {
	if strings.TrimSpace(rb.ID) == "" {
		return false, nil
	}
	var have string
	err := db.QueryRow(`SELECT updated_at FROM record_runbooks WHERE id = ?`, rb.ID).Scan(&have)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if have != "" {
		if t := parse(have); t != nil && t.After(rb.UpdatedAt) {
			return false, nil
		}
	}
	b, err := json.Marshal(rb)
	if err != nil {
		return false, err
	}
	// A slug is unique among live copies: a copy re-adopted under another
	// id replaces the one that held it.
	if _, err := db.Exec(`DELETE FROM record_runbooks WHERE lower(slug) = lower(?) AND id <> ?`, rb.Slug, rb.ID); err != nil {
		return false, err
	}
	_, err = db.Exec(`INSERT INTO record_runbooks (id, workspace_id, slug, source_ref, frozen, updated_at, row) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET workspace_id = excluded.workspace_id, slug = excluded.slug, source_ref = excluded.source_ref,
			frozen = excluded.frozen, updated_at = excluded.updated_at, row = excluded.row`,
		rb.ID, rb.WorkspaceID, rb.Slug, rb.SourceRef, rb.Frozen, ts(rb.UpdatedAt), string(b))
	return err == nil, err
}

// DeleteRunbookDB drops a copy that was un-adopted.
func DeleteRunbookDB(db *sql.DB, id string) (bool, error) {
	res, err := db.Exec(`DELETE FROM record_runbooks WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SaveRunbook caches a copy in p's record.
func SaveRunbook(p *project.Project, rb api.AdoptedRunbook) error {
	db, err := store.Open(p)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = SaveRunbookDB(db, rb)
	return err
}

// Runbooks lists the cached copies by slug.
func Runbooks(p *project.Project) ([]api.AdoptedRunbook, error) {
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT row FROM record_runbooks ORDER BY lower(slug)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.AdoptedRunbook{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var rb api.AdoptedRunbook
		if json.Unmarshal([]byte(raw), &rb) == nil {
			out = append(out, rb)
		}
	}
	return out, rows.Err()
}

// Runbook is one cached copy by slug, or nil.
func Runbook(p *project.Project, slug string) (*api.AdoptedRunbook, error) {
	all, err := Runbooks(p)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if strings.EqualFold(all[i].Slug, strings.TrimSpace(slug)) {
			return &all[i], nil
		}
	}
	return nil, nil
}

// RefuseAdopted is the authoring tools' guard: a slug that names an adopted
// copy is not a local draft. A frozen copy cannot be edited anywhere; an
// editable one is edited on gears-api with runbook_edit.
func RefuseAdopted(p *project.Project, slug string) error {
	rb, err := Runbook(p, slug)
	if err != nil || rb == nil {
		return nil
	}
	if rb.Frozen {
		return fmt.Errorf("runbook %s is a frozen copy of the official runbook %s and cannot be edited; start it as it is (runbook_start runbook=%s), or adopt an editable runbook",
			rb.Slug, rb.SourceRef, rb.Slug)
	}
	return fmt.Errorf("runbook %s is adopted from the official runbook %s and lives on gears-api; change it with runbook_edit (gears runbook edit %s), not the local authoring tools",
		rb.Slug, rb.SourceRef, rb.Slug)
}
