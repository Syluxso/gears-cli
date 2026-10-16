// Package runs is the local cache of runbook runs as gears-api records them
// (GRS-58). byz-orchestrator drives a run; gears-api keeps its state; this
// cache is what sync pulled and what the runbook tools last got back, for
// gears status and offline reads. Nothing here advances a run.
package runs

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
)

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func tsp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return ts(*t)
}

func parse(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return &t
}

// SaveDB writes a run and its steps when its version is higher than the
// cached one. applied says whether it did.
func SaveDB(db *sql.DB, r api.Run) (bool, error) {
	if strings.TrimSpace(r.ID) == "" {
		return false, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var have int
	err = tx.QueryRow(`SELECT version FROM record_runs WHERE id = ?`, r.ID).Scan(&have)
	if err == nil && have >= r.Version {
		return false, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO record_runs (id, workspace_id, runbook, status, current_step, card_key, agent_label, started_by, version, started_at, finished_at, updated_at,
			runbook_ref, runbook_version, internal, started_via, cancel_reason, canceled_by, canceled_label, canceled_via, canceled_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET workspace_id = excluded.workspace_id, runbook = excluded.runbook, status = excluded.status,
			current_step = excluded.current_step, card_key = excluded.card_key, agent_label = excluded.agent_label, started_by = excluded.started_by,
			version = excluded.version, started_at = excluded.started_at, finished_at = excluded.finished_at, updated_at = excluded.updated_at,
			runbook_ref = excluded.runbook_ref, runbook_version = excluded.runbook_version, internal = excluded.internal, started_via = excluded.started_via,
			cancel_reason = excluded.cancel_reason, canceled_by = excluded.canceled_by, canceled_label = excluded.canceled_label,
			canceled_via = excluded.canceled_via, canceled_at = excluded.canceled_at`,
		r.ID, r.WorkspaceID, r.Runbook, r.Status, r.CurrentStep, r.CardKey, r.AgentLabel, r.StartedBy, r.Version,
		ts(r.StartedAt), tsp(r.FinishedAt), ts(r.UpdatedAt), r.RunbookRef, r.RunbookVer, r.Internal, r.StartedVia,
		r.CancelReason, r.CanceledBy, r.CanceledLabel, r.CanceledVia, tsp(r.CanceledAt)); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM record_run_steps WHERE run_id = ?`, r.ID); err != nil {
		return false, err
	}
	for i, s := range r.Steps {
		ev := s.Evidence
		if ev == nil {
			ev = []api.Evidence{}
		}
		b, _ := json.Marshal(ev)
		pos := s.Position
		if pos == 0 {
			pos = i + 1
		}
		if _, err := tx.Exec(`INSERT INTO record_run_steps (run_id, step_id, position, kind, title, status, evidence, note, skip_reason, done_by, done_label, started_at, finished_at, done_via)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, s.StepID, pos, s.Kind, s.Title, s.Status, string(b), s.Note, s.SkipReason, s.DoneBy, s.DoneLabel, tsp(s.StartedAt), tsp(s.FinishedAt), s.DoneVia); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// Save is SaveDB on the project's cache.
func Save(p *project.Project, r api.Run) (bool, error) {
	db, err := store.Open(p)
	if err != nil {
		return false, err
	}
	defer db.Close()
	return SaveDB(db, r)
}

// Filter narrows List. Status "active" means running.
type Filter struct {
	Status  string
	Card    string
	Agent   string
	Runbook string // the run's name or registered ref
}

// List is the cached runs, newest first, with steps.
func List(p *project.Project, f Filter) ([]api.Run, error) {
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	status := strings.ToLower(strings.TrimSpace(f.Status))
	if status == "active" {
		status = "running"
	}
	rows, err := db.Query(`SELECT id, workspace_id, runbook, status, current_step, card_key, agent_label, started_by, version, started_at, finished_at, updated_at,
		runbook_ref, runbook_version, internal, started_via, cancel_reason, canceled_by, canceled_label, canceled_via, canceled_at
		FROM record_runs WHERE (? = '' OR status = ?) AND (? = '' OR lower(card_key) = lower(?)) AND (? = '' OR agent_label = ?)
		  AND (? = '' OR lower(runbook) = lower(?) OR lower(runbook_ref) = lower(?))
		ORDER BY started_at DESC, id DESC`, status, status, f.Card, f.Card, f.Agent, f.Agent, f.Runbook, f.Runbook, f.Runbook)
	if err != nil {
		return nil, err
	}
	var out []api.Run
	for rows.Next() {
		var r api.Run
		var started, finished, updated, canceled string
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.Runbook, &r.Status, &r.CurrentStep, &r.CardKey, &r.AgentLabel, &r.StartedBy, &r.Version,
			&started, &finished, &updated, &r.RunbookRef, &r.RunbookVer, &r.Internal, &r.StartedVia,
			&r.CancelReason, &r.CanceledBy, &r.CanceledLabel, &r.CanceledVia, &canceled); err != nil {
			rows.Close()
			return nil, err
		}
		if t := parse(started); t != nil {
			r.StartedAt = *t
		}
		if t := parse(updated); t != nil {
			r.UpdatedAt = *t
		}
		r.FinishedAt, r.CanceledAt = parse(finished), parse(canceled)
		out = append(out, r)
	}
	rows.Close()
	for i := range out {
		if err := steps(db, &out[i]); err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = []api.Run{}
	}
	return out, nil
}

func steps(db *sql.DB, r *api.Run) error {
	rows, err := db.Query(`SELECT step_id, position, kind, title, status, evidence, note, skip_reason, done_by, done_label, started_at, finished_at, done_via
		FROM record_run_steps WHERE run_id = ? ORDER BY position`, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.Steps = []api.RunStep{}
	for rows.Next() {
		s := api.RunStep{RunID: r.ID}
		var ev, started, finished string
		if err := rows.Scan(&s.StepID, &s.Position, &s.Kind, &s.Title, &s.Status, &ev, &s.Note, &s.SkipReason, &s.DoneBy, &s.DoneLabel, &started, &finished, &s.DoneVia); err != nil {
			return err
		}
		s.Evidence = []api.Evidence{}
		_ = json.Unmarshal([]byte(ev), &s.Evidence)
		s.StartedAt, s.FinishedAt = parse(started), parse(finished)
		r.Steps = append(r.Steps, s)
	}
	return rows.Err()
}

// Get is one cached run by id, or by a unique prefix of it (with or
// without "run-").
func Get(p *project.Project, key string) (*api.Run, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	all, err := List(p, Filter{})
	if err != nil {
		return nil, err
	}
	var hit *api.Run
	for i := range all {
		id := strings.ToLower(all[i].ID)
		if id == key {
			return &all[i], nil
		}
		if key != "" && (strings.HasPrefix(id, key) || strings.HasPrefix(strings.TrimPrefix(id, "run-"), key)) {
			if hit != nil {
				return nil, ErrAmbiguous
			}
			hit = &all[i]
		}
	}
	if hit == nil {
		return nil, ErrNotFound
	}
	return hit, nil
}

// Current is the run's current step, or nil.
func Current(r *api.Run) *api.RunStep {
	for i := range r.Steps {
		if r.Steps[i].Status == "current" {
			return &r.Steps[i]
		}
	}
	return nil
}

type runErr string

func (e runErr) Error() string { return string(e) }

const (
	ErrNotFound  = runErr("no such run in the local cache; gears sync, or pass the full run id")
	ErrAmbiguous = runErr("more than one cached run starts with that; give more of the id")
)
