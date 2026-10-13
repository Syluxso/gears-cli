// Package sync moves the local cache to and from gears-api. Push first
// (rows changed since the last push), then pull (changes after the saved
// cursor) and apply. The server is the authority; a pulled row overwrites
// the local one. Desk, sessions and artifacts sync; cards and runs do not
// yet (not in slice 1).
package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/gitx"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/store"
)

// Report is what one sync did.
type Report struct {
	Host        string   `json:"host"`
	Workspace   string   `json:"workspace"`
	Project     string   `json:"project"`
	Pushed      int      `json:"pushed"`
	Skipped     int      `json:"skipped"`
	Failed      int      `json:"failed"`
	Pulled      int      `json:"pulled"`
	Applied     int      `json:"applied"`
	Cursor      int64    `json:"cursor"`
	ContentUp   int      `json:"contentUploaded"`
	ContentDown int      `json:"contentDownloaded"`
	Notes       []string `json:"notes"`
}

// Reconcile is what gears login does after the token is saved: make sure
// the workspace and this project exist in the account, remember the
// workspace id in project.yaml, and say whether there is anything to pull.
type ReconcileOut struct {
	Workspace     api.Workspace `json:"workspace"`
	Project       *api.Project  `json:"project,omitempty"`
	WorkspaceNew  bool          `json:"workspaceCreated"`
	ProjectNew    bool          `json:"projectCreated"`
	Pending       int           `json:"pendingChanges"`
	BoundByRemote bool          `json:"boundByRemote"`
}

func Reconcile(ctx context.Context, c *api.Client, p *project.Project) (*ReconcileOut, error) {
	out := &ReconcileOut{}
	ws := workspaceOf(p)
	dirty := false

	// A fresh clone with no workspace field and no ancestor binding: ask the
	// API by remote. If the account already knows this remote, adopt its ids.
	if p.Workspace == "" && p.Ancestor() == nil {
		if remote := gitx.RemoteURL(p.Root); remote != "" {
			if b, err := c.Bind(ctx, remote); err == nil && b != nil {
				if b.Project.ID != p.ID {
					p.ID = b.Project.ID
					dirty = true
				}
				if b.Workspace.Prefix != "" && b.Workspace.Prefix != p.Prefix {
					p.Prefix = b.Workspace.Prefix
					dirty = true
				}
				ws = b.Workspace.ID
				out.BoundByRemote = true
			}
		}
	}

	// Workspace: get, else create. Never rewrite an existing one from here.
	w, err := c.GetWorkspace(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if w == nil {
		w, _, err = c.UpsertWorkspace(ctx, api.Workspace{ID: ws, Slug: workspaceSlug(p), Prefix: p.Prefix})
		if err != nil {
			return nil, fmt.Errorf("workspace: %w", err)
		}
		out.WorkspaceNew = true
	}
	out.Workspace = *w

	// Project: same, when this binding is a project inside the workspace.
	if ws != p.ID {
		pr, err := c.GetProject(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("project: %w", err)
		}
		remote := gitx.RemoteURL(p.Root)
		if pr == nil {
			pr, _, err = c.UpsertProject(ctx, api.Project{ID: p.ID, WorkspaceID: ws, Slug: p.Slug, GitRemote: remote})
			if err != nil {
				return nil, fmt.Errorf("project: %w", err)
			}
			out.ProjectNew = true
		} else if pr.GitRemote == "" && remote != "" {
			pr, _, _ = c.UpsertProject(ctx, api.Project{ID: pr.ID, WorkspaceID: pr.WorkspaceID, Slug: pr.Slug, Name: pr.Name, GitRemote: remote})
		}
		out.Project = pr
	}

	if p.Workspace != ws {
		p.Workspace = ws
		dirty = true
	}
	if dirty {
		if err := p.Save(); err != nil {
			return nil, err
		}
	}

	cursor, _ := state(p, "cursor")
	var cur int64
	fmt.Sscan(cursor, &cur)
	if pull, err := c.Pull(ctx, ws, cur); err == nil {
		for _, ch := range pull.Changes {
			if ch.Table != "workspaces" && ch.Table != "projects" {
				out.Pending++
			}
		}
		if pull.More {
			out.Pending++
		}
	}
	return out, nil
}

// workspaceOf is the workspace id this project syncs to.
func workspaceOf(p *project.Project) string { return p.WorkspaceID() }

func workspaceSlug(p *project.Project) string {
	if p.IsWorkspace() {
		return p.Slug
	}
	if anc := p.Ancestor(); anc != nil {
		return anc.Slug
	}
	return p.Slug
}

// Run pushes then pulls.
func Run(ctx context.Context, c *api.Client, p *project.Project) (*Report, error) {
	ws := workspaceOf(p)
	if p.Workspace == "" {
		if _, err := Reconcile(ctx, c, p); err != nil {
			return nil, err
		}
		ws = workspaceOf(p)
	}
	r := &Report{Host: c.Host, Workspace: ws, Project: p.ID, Notes: []string{}}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if err := push(ctx, c, p, db, ws, r); err != nil {
		return r, err
	}
	if err := pull(ctx, c, p, db, ws, r); err != nil {
		return r, err
	}
	return r, nil
}

// ------------------------------------------------------------------ push

func push(ctx context.Context, c *api.Client, p *project.Project, db *sql.DB, ws string, r *Report) error {
	since, _ := stateDB(db, "pushed_at")
	now := store.Now()
	projectID := ""
	if !p.IsWorkspace() {
		projectID = p.ID
	}
	in := api.PushIn{Rows: map[string][]any{}}

	// Desk items: anything created or closed since the last push.
	rows, err := db.Query(`SELECT id, type, from_agent, to_agent, text, ref, created_at, state, closed_at, answer, updated_at FROM desk_items
		WHERE created_at > ? OR closed_at > ? OR updated_at > ?`, since, since, since)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, typ, from, to, text, ref, created, state, closed, answer, updated string
		if err := rows.Scan(&id, &typ, &from, &to, &text, &ref, &created, &state, &closed, &answer, &updated); err != nil {
			rows.Close()
			return err
		}
		row := map[string]any{"id": id, "type": typ, "from": from, "to": to, "text": text, "ref": ref, "state": state, "answer": answer, "createdAt": created,
			"updatedAt": latest(created, closed, updated)}
		if closed != "" {
			row["closedAt"] = closed
		}
		in.Rows["desk_items"] = append(in.Rows["desk_items"], row)
	}
	rows.Close()

	// Session entries: rows from before ids were minted at append get one,
	// then the new ones are pushed.
	if err := session.EnsureUIDs(db); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT uid, at, agent, kind, text, project_id FROM session_entries WHERE at > ?`, since)
	if err != nil {
		return err
	}
	for rows.Next() {
		var uid, at, agent, kind, text, proj string
		if err := rows.Scan(&uid, &at, &agent, &kind, &text, &proj); err != nil {
			rows.Close()
			return err
		}
		row := map[string]any{"id": uid, "agent": agent, "kind": kind, "text": text, "at": at, "updatedAt": at}
		if proj == "" {
			proj = projectID
		}
		if proj != "" {
			row["projectId"] = proj
		}
		in.Rows["session_entries"] = append(in.Rows["session_entries"], row)
	}
	rows.Close()

	// Artifacts: rows updated since; content uploaded after the row lands.
	arts, err := artifact.List(p)
	if err != nil {
		return err
	}
	var contentFor []artifact.Artifact
	for _, a := range arts {
		updated := a.UpdatedAt.UTC().Format(time.RFC3339)
		if since != "" && updated <= since {
			continue
		}
		row := map[string]any{"id": a.ID, "prefix": a.Prefix, "slug": a.Slug, "title": a.Title, "status": a.Status, "tags": a.Tags,
			"bodyHash": a.BodyHash, "shapeOk": a.ShapeOK, "updatedAt": updated}
		if projectID != "" {
			row["projectId"] = projectID
		}
		in.Rows["artifacts"] = append(in.Rows["artifacts"], row)
		if a.FilePresent {
			contentFor = append(contentFor, a)
		}
	}

	total := len(in.Rows["desk_items"]) + len(in.Rows["session_entries"]) + len(in.Rows["artifacts"])
	if total > 0 {
		out, err := c.Push(ctx, ws, in)
		if err != nil {
			return fmt.Errorf("push: %w", err)
		}
		r.Pushed, r.Skipped, r.Failed = out.Applied, out.Skipped, out.Failed
		for _, res := range out.Results {
			if res.Error != "" {
				r.Notes = append(r.Notes, fmt.Sprintf("push %s %s: %s", res.Table, short(res.ID), res.Error))
			}
		}
	}
	// Content after rows, so the row exists server-side.
	for _, a := range contentFor {
		body, err := os.ReadFile(a.Path)
		if err != nil {
			continue
		}
		fileID, ok, err := c.PutArtifactContent(ctx, a.ID, body)
		if err != nil {
			r.Notes = append(r.Notes, "content "+a.File+": "+err.Error())
			continue
		}
		if !ok {
			r.Notes = append(r.Notes, "content not uploaded: server has no file store configured")
			break
		}
		r.ContentUp++
		if fileID != "" {
			_, _ = db.Exec(`UPDATE artifacts SET remote_file_id = ? WHERE id = ?`, fileID, a.ID)
		}
	}
	return setState(db, "pushed_at", now)
}

// ------------------------------------------------------------------ pull

func pull(ctx context.Context, c *api.Client, p *project.Project, db *sql.DB, ws string, r *Report) error {
	cursorStr, _ := stateDB(db, "cursor")
	var cursor int64
	fmt.Sscan(cursorStr, &cursor)
	for {
		out, err := c.Pull(ctx, ws, cursor)
		if err != nil {
			return fmt.Errorf("pull: %w", err)
		}
		for _, ch := range out.Changes {
			r.Pulled++
			applied, err := apply(ctx, c, p, db, ch, r)
			if err != nil {
				r.Notes = append(r.Notes, fmt.Sprintf("apply %s %s: %s", ch.Table, short(ch.ID), err))
				continue
			}
			if applied {
				r.Applied++
			}
		}
		if out.Cursor > cursor {
			cursor = out.Cursor
		}
		if err := setState(db, "cursor", fmt.Sprint(cursor)); err != nil {
			return err
		}
		r.Cursor = cursor
		if !out.More || len(out.Changes) == 0 {
			return nil
		}
	}
}

func apply(ctx context.Context, c *api.Client, p *project.Project, db *sql.DB, ch api.Change, r *Report) (bool, error) {
	switch ch.Table {
	case "desk_items":
		if ch.Deleted {
			_, err := db.Exec(`DELETE FROM desk_items WHERE id = ?`, ch.ID)
			return true, err
		}
		var d struct {
			ID, Type, From, To, Text, Ref, State, Answer string
			CreatedAt, UpdatedAt                         time.Time
			ClosedAt                                     *time.Time
		}
		if err := json.Unmarshal(ch.Row, &d); err != nil {
			return false, err
		}
		closed := ""
		if d.ClosedAt != nil {
			closed = d.ClosedAt.UTC().Format(time.RFC3339)
		}
		_, err := db.Exec(`INSERT INTO desk_items (id, type, from_agent, to_agent, text, ref, created_at, state, closed_at, closed_by, answer, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)
			ON CONFLICT(id) DO UPDATE SET type = excluded.type, from_agent = excluded.from_agent, to_agent = excluded.to_agent, text = excluded.text,
				ref = excluded.ref, state = excluded.state, closed_at = excluded.closed_at, answer = excluded.answer, updated_at = excluded.updated_at`,
			d.ID, d.Type, d.From, d.To, d.Text, d.Ref, d.CreatedAt.UTC().Format(time.RFC3339), d.State, closed, d.Answer, d.UpdatedAt.UTC().Format(time.RFC3339))
		return true, err
	case "session_entries":
		if ch.Deleted {
			_, err := db.Exec(`DELETE FROM session_entries WHERE uid = ?`, ch.ID)
			return true, err
		}
		var e struct {
			ID, Agent, Kind, Text string
			ProjectID             *string
			At                    time.Time
		}
		if err := json.Unmarshal(ch.Row, &e); err != nil {
			return false, err
		}
		proj := ""
		if e.ProjectID != nil {
			proj = *e.ProjectID
		}
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM session_entries WHERE uid = ?`, e.ID).Scan(&n)
		at := e.At.UTC().Format(time.RFC3339)
		if n > 0 {
			_, err := db.Exec(`UPDATE session_entries SET at = ?, agent = ?, kind = ?, text = ?, project_id = ? WHERE uid = ?`, at, e.Agent, e.Kind, e.Text, proj, e.ID)
			return true, err
		}
		_, err := db.Exec(`INSERT INTO session_entries (at, agent, kind, text, uid, project_id) VALUES (?, ?, ?, ?, ?, ?)`, at, e.Agent, e.Kind, e.Text, e.ID, proj)
		return true, err
	case "artifacts":
		var a struct {
			ID, Prefix, Slug, Title, Status, BodyHash, FileID, CreatedBy string
			ProjectID                                                    *string
			Tags                                                         []string
			ShapeOK                                                      bool
			CreatedAt, UpdatedAt                                         time.Time
		}
		if ch.Deleted {
			_, err := db.Exec(`DELETE FROM artifacts WHERE id = ?`, ch.ID)
			return true, err
		}
		if err := json.Unmarshal(ch.Row, &a); err != nil {
			return false, err
		}
		// Only this project's artifacts land in this cache (or workspace-level ones).
		mine := a.ProjectID == nil || *a.ProjectID == "" || *a.ProjectID == p.ID
		if !mine {
			return false, nil
		}
		file := a.Prefix + "--" + a.Slug + artifact.Ext(a.Prefix)
		path := filepath.Join(p.ArtifactsDir(), file)
		_, statErr := os.Stat(path)
		present := statErr == nil
		if _, err := db.Exec(`INSERT INTO artifacts (id, prefix, slug, file, title, status, created_at, created_by, updated_at, body_hash, file_present, shape_ok, shape_errors, remote_file_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?)
			ON CONFLICT(id) DO UPDATE SET prefix = excluded.prefix, slug = excluded.slug, file = excluded.file, title = excluded.title, status = excluded.status,
				updated_at = excluded.updated_at, body_hash = excluded.body_hash, shape_ok = excluded.shape_ok, remote_file_id = excluded.remote_file_id`,
			a.ID, a.Prefix, a.Slug, file, a.Title, a.Status, a.CreatedAt.UTC().Format(time.RFC3339), a.CreatedBy, a.UpdatedAt.UTC().Format(time.RFC3339),
			a.BodyHash, b2i(present), b2i(a.ShapeOK), a.FileID); err != nil {
			return false, err
		}
		_, _ = db.Exec(`DELETE FROM artifact_tags WHERE artifact_id = ?`, a.ID)
		for _, t := range a.Tags {
			_, _ = db.Exec(`INSERT OR IGNORE INTO artifact_tags (artifact_id, tag) VALUES (?, ?)`, a.ID, t)
		}
		// Body: fetch when the local file is missing or stale and the server has one.
		localHash := ""
		if present {
			if b, err := os.ReadFile(path); err == nil {
				localHash = artifact.Hash(string(b))
			}
		}
		if a.FileID != "" && localHash != a.BodyHash {
			body, ok, err := c.GetArtifactContent(ctx, a.ID)
			if err != nil {
				r.Notes = append(r.Notes, "content "+file+": "+err.Error())
			} else if ok {
				if err := os.MkdirAll(p.ArtifactsDir(), 0o755); err == nil && os.WriteFile(path, body, 0o644) == nil {
					r.ContentDown++
					_, _ = db.Exec(`UPDATE artifacts SET file_present = 1 WHERE id = ?`, a.ID)
				}
			}
		}
		return true, nil
	case "workspaces", "projects":
		// Binding is local; nothing to apply.
		return false, nil
	}
	return false, nil
}

// ----------------------------------------------------------------- state

func state(p *project.Project, key string) (string, error) {
	if !store.Exists(p) {
		return "", nil
	}
	db, err := store.Open(p)
	if err != nil {
		return "", err
	}
	defer db.Close()
	return stateDB(db, key)
}

func stateDB(db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func setState(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO sync_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func latest(ts ...string) string {
	out := ""
	for _, t := range ts {
		if t > out {
			out = t
		}
	}
	return out
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

var _ = strings.TrimSpace
