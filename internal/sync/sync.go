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
	Host        string `json:"host"`
	Workspace   string `json:"workspace"`
	Project     string `json:"project"`
	Pushed      int    `json:"pushed"`
	Skipped     int    `json:"skipped"`
	Failed      int    `json:"failed"`
	Pulled      int    `json:"pulled"`
	Applied     int    `json:"applied"`
	Cursor      int64  `json:"cursor"`
	ContentUp   int    `json:"contentUploaded"`
	ContentDown int    `json:"contentDownloaded"`
	// Projects is how many manifest projects were checked against the API
	// (one GET each); ProjectsCreated how many of them it did not have.
	Projects        int      `json:"projects"`
	ProjectsCreated int      `json:"projectsCreated"`
	Notes           []string `json:"notes"`
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
	// At a workspace root: the manifest projects checked and created.
	Projects        int      `json:"projects"`
	ProjectsCreated int      `json:"projectsCreated"`
	Notes           []string `json:"notes,omitempty"`
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

	// Projects: a standalone project binding makes sure of itself; the
	// workspace root makes sure of every project in its manifest.
	if ws != p.ID {
		pr, created, err := ensureProject(ctx, c, p, ws)
		if err != nil {
			return nil, err
		}
		out.Project, out.ProjectNew = pr, created
	} else {
		mr, err := ensureManifest(ctx, c, p, ws)
		if err != nil {
			return nil, err
		}
		out.Projects, out.ProjectsCreated, out.Notes = mr.checked, mr.created, mr.notes
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

// ensureProject makes sure a standalone project binding (a clone bound by
// remote, no workspace above it) exists in the account. See ensureOne.
func ensureProject(ctx context.Context, c *api.Client, p *project.Project, ws string) (*api.Project, bool, error) {
	return ensureOne(ctx, c, ws, p.ID, p.Slug, gitx.RemoteURL(p.Root))
}

// ensureOne makes sure project id exists in the account under workspace ws
// with the current git remote. Create when the API has no such project;
// refresh only the remote (keeping the API's slug and name) when it differs
// after normalisation; otherwise one GET and nothing written, so repeated
// syncs are idempotent. Projects stay out of the general push: they are
// bindings, not LWW rows (GRS-27). The workspace itself is never a project.
func ensureOne(ctx context.Context, c *api.Client, ws, id, slug, remote string) (*api.Project, bool, error) {
	pr, err := c.GetProject(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("project %s: %w", slug, err)
	}
	return ensureFetched(ctx, c, ws, id, slug, remote, pr)
}

// ensureFetched is ensureOne after the GET: pr is the API's row, or nil.
func ensureFetched(ctx context.Context, c *api.Client, ws, id, slug, remote string, pr *api.Project) (*api.Project, bool, error) {
	var err error
	if pr == nil {
		pr, _, err = c.UpsertProject(ctx, api.Project{ID: id, WorkspaceID: ws, Slug: slug, GitRemote: remote})
		if err != nil {
			return nil, false, fmt.Errorf("project %s: %w", slug, err)
		}
		return pr, true, nil
	}
	if remote != "" && gitx.NormalizeRemote(pr.GitRemote) != gitx.NormalizeRemote(remote) {
		up, _, err := c.UpsertProject(ctx, api.Project{ID: pr.ID, WorkspaceID: pr.WorkspaceID, Slug: pr.Slug, Name: pr.Name, GitRemote: remote})
		if err != nil {
			return nil, false, fmt.Errorf("project %s: %w", slug, err)
		}
		pr = up
	}
	return pr, false, nil
}

type manifestReport struct {
	checked, created int
	notes            []string
}

// ensureManifest makes sure every non-missing project in the workspace
// root's manifest exists in the account (GRS-40): one GET each, create if
// absent, refresh the remote if it moved. An id the API does not have, for
// a remote it does know in this workspace (the scan ran offline and minted
// one), gives way to the API's id: the manifest and the cache's rows are
// retagged before anything is pushed under it.
func ensureManifest(ctx context.Context, c *api.Client, p *project.Project, ws string) (*manifestReport, error) {
	out := &manifestReport{}
	m, err := project.LoadManifest(p)
	if err != nil {
		return nil, err
	}
	dirty := false
	for i := range m.Projects {
		e := &m.Projects[i]
		if e.Missing {
			continue
		}
		if e.ID == ws {
			out.notes = append(out.notes, "project "+e.Slug+" has the workspace's id; not a project row, skipped")
			continue
		}
		// The live remote when the repo is there, else the manifest's.
		dir := filepath.Join(p.Root, filepath.FromSlash(e.Path))
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			if live := gitx.RemoteURL(dir); live != e.Remote {
				e.Remote, dirty = live, true
			}
		}
		out.checked++
		pr, err := c.GetProject(ctx, e.ID)
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", e.Slug, err)
		}
		if pr == nil && e.Remote != "" {
			b, err := c.Bind(ctx, e.Remote)
			if err != nil {
				return nil, fmt.Errorf("project %s: %w", e.Slug, err)
			}
			if b != nil && b.Workspace.ID == ws && m.ByID(b.Project.ID) == nil {
				old := e.ID
				if err := retag(p, old, b.Project.ID); err != nil {
					return nil, err
				}
				e.ID, dirty = b.Project.ID, true
				pr = &b.Project
				out.notes = append(out.notes, fmt.Sprintf("project %s: adopted the API's id %s for %s (was %s)", e.Slug, e.ID, e.Remote, old))
			}
		}
		pr, created, err := ensureFetched(ctx, c, ws, e.ID, e.Slug, e.Remote, pr)
		if err != nil {
			return nil, err
		}
		if created {
			out.created++
			out.notes = append(out.notes, "project "+e.Slug+" created in the account ("+short(pr.ID)+")")
		} else if pr.WorkspaceID != ws {
			out.notes = append(out.notes, "project "+e.Slug+" belongs to another workspace on the API; left alone")
		}
	}
	if dirty {
		if err := m.Save(p); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// retag moves the cache's rows from a project id to the one the API holds.
func retag(p *project.Project, from, to string) error {
	if !store.Exists(p) {
		return nil
	}
	db, err := store.Open(p)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, tbl := range []string{"session_entries", "artifacts"} {
		if _, err := db.Exec(`UPDATE `+tbl+` SET project_id = ? WHERE project_id = ?`, to, from); err != nil {
			return err
		}
	}
	return nil
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
	var rec *ReconcileOut
	if p.Workspace == "" {
		// Reconcile binds and makes sure workspace and projects exist.
		var err error
		if rec, err = Reconcile(ctx, c, p); err != nil {
			return nil, err
		}
		ws = workspaceOf(p)
	}
	r := &Report{Host: c.Host, Workspace: ws, Project: p.ID, Notes: []string{}}
	switch {
	case rec != nil:
		r.Projects, r.ProjectsCreated = rec.Projects, rec.ProjectsCreated
		r.Notes = append(r.Notes, rec.Notes...)
	case p.IsWorkspace():
		// Already bound: every sync still makes sure each manifest project
		// is in the account, so a repo scanned offline (or whose remote
		// moved) shows up without another gears login (GRS-27, GRS-40).
		// Before the push: rows tagged with a project need it to exist.
		mr, err := ensureManifest(ctx, c, p, ws)
		if err != nil {
			return nil, err
		}
		r.Projects, r.ProjectsCreated = mr.checked, mr.created
		r.Notes = append(r.Notes, mr.notes...)
	default:
		// A standalone project binding makes sure of itself.
		if _, _, err := ensureProject(ctx, c, p, ws); err != nil {
			return nil, err
		}
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if err := push(ctx, c, p, db, ws, r); err != nil {
		return r, err
	}
	// GRS-40: the cache holds every artifact of the workspace. A cache that
	// kept only its own binding's (GRS-26) let its cursor pass the rest, so
	// it pulls the workspace from the start, once. Pulled rows are upserts,
	// and local changes went up in the push above.
	scope, _ := stateDB(db, "artifact_scope")
	if scope != "workspace" {
		if cur, _ := stateDB(db, "cursor"); cur != "" && cur != "0" {
			r.Notes = append(r.Notes, "this cache now holds every artifact of the workspace: pulling from the start, once (cursor "+cur+" -> 0)")
			if err := setState(db, "cursor", "0"); err != nil {
				return r, err
			}
		}
	}
	if err := pull(ctx, c, p, db, ws, r); err != nil {
		return r, err
	}
	if scope != "workspace" {
		if err := setState(db, "artifact_scope", "workspace"); err != nil {
			return r, err
		}
	}
	return r, nil
}

// ------------------------------------------------------------------ push

func push(ctx context.Context, c *api.Client, p *project.Project, db *sql.DB, ws string, r *Report) error {
	// Timestamps are to the second, and pushed_at is taken as the push
	// starts, so a row written later in that same second carries the same
	// stamp. The window is inclusive (>= since): such a row goes up on the
	// next sync instead of never, and a row re-sent once is a no-op upsert.
	since, _ := stateDB(db, "pushed_at")
	now := store.Now()
	projectID := ""
	if !p.IsWorkspace() {
		projectID = p.ID
	}
	in := api.PushIn{Rows: map[string][]any{}}

	// Desk items: anything created or closed since the last push.
	rows, err := db.Query(`SELECT id, type, from_agent, to_agent, text, ref, created_at, state, closed_at, answer, updated_at FROM desk_items
		WHERE created_at >= ? OR closed_at >= ? OR updated_at >= ?`, since, since, since)
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
	rows, err = db.Query(`SELECT uid, at, agent, kind, text, project_id FROM session_entries WHERE at >= ?`, since)
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
		if since != "" && updated < since {
			continue
		}
		row := map[string]any{"id": a.ID, "prefix": a.Prefix, "slug": a.Slug, "title": a.Title, "status": a.Status, "tags": a.Tags,
			"bodyHash": a.BodyHash, "shapeOk": a.ShapeOK, "updatedAt": updated}
		// The row's own project, never the binding's: one cache holds every
		// project's artifacts and the workspace-level ones (GRS-40).
		if a.ProjectID != "" {
			row["projectId"] = a.ProjectID
		}
		in.Rows["artifacts"] = append(in.Rows["artifacts"], row)
		if a.FilePresent {
			contentFor = append(contentFor, a)
		}
	}

	// Deletes: tombstones left by a local delete, pushed until the server
	// takes them. A delete the server has no row for is skipped, not failed,
	// so a row deleted before it was ever pushed clears too.
	tombs, err := db.Query(`SELECT tbl, id FROM sync_deletes`)
	if err != nil {
		return err
	}
	nDeletes := 0
	for tombs.Next() {
		var tbl, id string
		if err := tombs.Scan(&tbl, &id); err != nil {
			tombs.Close()
			return err
		}
		if in.Deletes == nil {
			in.Deletes = map[string][]string{}
		}
		in.Deletes[tbl] = append(in.Deletes[tbl], id)
		nDeletes++
	}
	tombs.Close()

	total := len(in.Rows["desk_items"]) + len(in.Rows["session_entries"]) + len(in.Rows["artifacts"]) + nDeletes
	if total > 0 {
		out, err := c.Push(ctx, ws, in)
		if err != nil {
			return fmt.Errorf("push: %w", err)
		}
		r.Pushed, r.Skipped, r.Failed = out.Applied, out.Skipped, out.Failed
		failed := map[string]bool{}
		for _, res := range out.Results {
			if res.Error != "" {
				failed[res.Table+"/"+res.ID] = true
				r.Notes = append(r.Notes, fmt.Sprintf("push %s %s: %s", res.Table, short(res.ID), res.Error))
			}
		}
		for tbl, ids := range in.Deletes {
			for _, id := range ids {
				if failed[tbl+"/"+id] {
					continue
				}
				_, _ = db.Exec(`DELETE FROM sync_deletes WHERE tbl = ? AND id = ?`, tbl, id)
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
			// Row and file together, or Reconcile would re-adopt the file.
			return true, artifact.Forget(db, p, ch.ID)
		}
		if err := json.Unmarshal(ch.Row, &a); err != nil {
			return false, err
		}
		// The cache holds every artifact of the workspace, each tagged with
		// its project (empty: workspace-level). GRS-40 replaces GRS-26's
		// one-binding-per-cache rule.
		pid := ""
		if a.ProjectID != nil {
			pid = *a.ProjectID
		}
		// Deleted here and the delete not yet pushed: do not bring it back.
		var tomb int
		_ = db.QueryRow(`SELECT COUNT(*) FROM sync_deletes WHERE tbl = 'artifacts' AND id = ?`, a.ID).Scan(&tomb)
		if tomb > 0 {
			return false, nil
		}
		file, err := cacheFile(db, p, a.ID, a.Prefix, a.Slug, pid, r)
		if err != nil {
			return false, err
		}
		path := filepath.Join(p.ArtifactsDir(), file)
		_, statErr := os.Stat(path)
		present := statErr == nil
		if _, err := db.Exec(`INSERT INTO artifacts (id, prefix, slug, file, title, status, created_at, created_by, updated_at, body_hash, file_present, shape_ok, shape_errors, remote_file_id, project_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)
			ON CONFLICT(id) DO UPDATE SET prefix = excluded.prefix, slug = excluded.slug, file = excluded.file, title = excluded.title, status = excluded.status,
				updated_at = excluded.updated_at, body_hash = excluded.body_hash, shape_ok = excluded.shape_ok, remote_file_id = excluded.remote_file_id,
				project_id = excluded.project_id`,
			a.ID, a.Prefix, a.Slug, file, a.Title, a.Status, a.CreatedAt.UTC().Format(time.RFC3339), a.CreatedBy, a.UpdatedAt.UTC().Format(time.RFC3339),
			a.BodyHash, b2i(present), b2i(a.ShapeOK), a.FileID, pid); err != nil {
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
		fetched := false
		if a.FileID != "" && localHash != a.BodyHash {
			body, ok, err := c.GetArtifactContent(ctx, a.ID)
			if err != nil {
				r.Notes = append(r.Notes, "content "+file+": "+err.Error())
			} else if ok {
				if err := os.MkdirAll(p.ArtifactsDir(), 0o755); err == nil && os.WriteFile(path, body, 0o644) == nil {
					r.ContentDown++
					fetched = true
					_, _ = db.Exec(`UPDATE artifacts SET file_present = 1 WHERE id = ?`, a.ID)
				}
			}
		}
		// No body to fetch (no file store, or none uploaded) and the cache
		// holds a different one: keep the row on the local hash, so
		// Reconcile does not read the file as an edit and push it over the
		// server's row. A missing file is regenerated the same way.
		if present && !fetched && localHash != "" && localHash != a.BodyHash {
			_, _ = db.Exec(`UPDATE artifacts SET body_hash = ? WHERE id = ?`, localHash, a.ID)
		}
		return true, nil
	case "workspaces", "projects":
		// Binding is local; nothing to apply.
		return false, nil
	}
	return false, nil
}

// cacheFile is the file name a pulled artifact is cached under. Artifacts
// are flat in the workspace's one .gears/artifacts/, so a name is unique
// across the workspace: artifact new refuses one already taken. A row made
// elsewhere (a standalone clone, or a project binding from before GRS-40)
// can still arrive with a name another artifact here holds; it is cached as
// <prefix>--<slug>--<project slug>.<ext> (the id's tail when there is no
// project slug to use), and the row keeps its server slug, so nothing is
// renamed on the API and neither artifact is lost. A row already cached
// keeps its name.
func cacheFile(db *sql.DB, p *project.Project, id, prefix, slug, projectID string, r *Report) (string, error) {
	base, ext := prefix+"--"+slug, artifact.Ext(prefix)
	var have string
	err := db.QueryRow(`SELECT file FROM artifacts WHERE id = ?`, id).Scan(&have)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if have == base+ext || (strings.HasPrefix(have, base+"--") && strings.HasSuffix(have, ext)) {
		return have, nil
	}
	taken := func(file string) bool {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE file = ? AND id != ?`, file, id).Scan(&n)
		return n > 0
	}
	if !taken(base + ext) {
		return base + ext, nil
	}
	tag := ""
	if projectID != "" && p.IsWorkspace() {
		if m, err := project.LoadManifest(p); err == nil {
			if e := m.ByID(projectID); e != nil {
				tag = e.Slug
			}
		}
	}
	file := base + "--" + tag + ext
	if tag == "" || taken(file) {
		file = base + "--" + id[len(id)-8:] + ext
	}
	r.Notes = append(r.Notes, fmt.Sprintf("artifact %s: %s is taken by another artifact in this workspace; cached as %s", short(id), base+ext, file))
	return file, nil
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
