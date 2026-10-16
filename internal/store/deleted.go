package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/Syluxso/gears-cli/internal/project"
)

// GRS-80: gears-api soft deletes a workspace. When a sync (or login, or the
// live board) hears 410 gears.workspace_deleted, the cache is marked
// deleted: the local data stays, and every local read refuses with a plain
// message until a restore (or a later successful pull) clears the mark.

// deletedKey is the sync_state key holding the deleted workspace's slug.
const deletedKey = "workspace_deleted"

// ErrWorkspaceDeleted matches every DeletedError.
var ErrWorkspaceDeleted = errors.New("workspace deleted on gears-api")

// DeletedError is the refusal a marked cache answers.
type DeletedError struct{ Slug string }

func (e *DeletedError) Error() string {
	return "workspace " + e.Slug + " was deleted on gears-api (restore: gears workspace restore)"
}

func (e *DeletedError) Is(target error) bool { return target == ErrWorkspaceDeleted }

func checkLive(db *sql.DB) error {
	var slug string
	err := db.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, deletedKey).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && strings.TrimSpace(slug) == "") {
		return nil
	}
	if err != nil {
		return err
	}
	return &DeletedError{Slug: slug}
}

// CheckLive answers the DeletedError when the cache is marked deleted, nil
// otherwise (and when there is no cache yet).
func CheckLive(p *project.Project) error {
	if !Exists(p) {
		return nil
	}
	db, err := OpenAny(p)
	if err != nil {
		return err
	}
	defer db.Close()
	return checkLive(db)
}

// MarkDeleted marks the cache deleted under slug and answers the refusal.
func MarkDeleted(p *project.Project, slug string) error {
	db, err := OpenAny(p)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := MarkDeletedDB(db, slug); err != nil {
		return err
	}
	return &DeletedError{Slug: slug}
}

// MarkDeletedDB is MarkDeleted on an open cache.
func MarkDeletedDB(db *sql.DB, slug string) error {
	if strings.TrimSpace(slug) == "" {
		slug = "?"
	}
	_, err := db.Exec(`INSERT INTO sync_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, deletedKey, slug)
	return err
}

// ClearDeleted removes the mark; true when there was one.
func ClearDeleted(p *project.Project) (bool, error) {
	if !Exists(p) {
		return false, nil
	}
	db, err := OpenAny(p)
	if err != nil {
		return false, err
	}
	defer db.Close()
	return ClearDeletedDB(db)
}

// ClearDeletedDB is ClearDeleted on an open cache.
func ClearDeletedDB(db *sql.DB) (bool, error) {
	res, err := db.Exec(`DELETE FROM sync_state WHERE key = ?`, deletedKey)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
