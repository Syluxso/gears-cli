// Package sync moves the local cache to and from gears-api. Push first
// (rows changed since the last push), then pull (changes after the saved
// cursor) and apply. The server is the authority; a pulled row overwrites
// the local one. Desk, sessions, artifacts and the board (GRS-47, see
// cards.go) sync; runs do not.
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
	Projects        int `json:"projects"`
	ProjectsCreated int `json:"projectsCreated"`
	// Cards is "synced", or "skipped" when the API has no card tables (or
	// the board could not be read); CardsRenumbered counts local cards the
	// API gave another number.
	Cards           string   `json:"cards"`
	CardsRenumbered int      `json:"cardsRenumbered"`
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

// Run pushes then pulls. It holds this process's sync lock throughout
// (GRS-25): a second Run, a card write-through and the live stream's pull
// wait for it, so syncs never overlap.
func Run(ctx context.Context, c *api.Client, p *project.Project) (*Report, error) {
	if err := cardsMu.lock(ctx); err != nil {
		return nil, err
	}
	defer cardsMu.unlock()
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

	cs, err := prepareCards(ctx, c, p, db, ws, r)
	if err != nil {
		return r, err
	}
	if err := push(ctx, c, p, db, ws, r, cs); err != nil {
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
	// GRS-47: a CLI from before cards sync passed card changes by without
	// applying them. The first sync that syncs cards pulls from the start,
	// once, so the board arrives whole.
	cardScope, _ := stateDB(db, "cards_scope")
	if cs.ok && cardScope != "workspace" {
		if cur, _ := stateDB(db, "cursor"); cur != "" && cur != "0" {
			r.Notes = append(r.Notes, "first sync with cards: pulling from the start, once (cursor "+cur+" -> 0)")
			if err := setState(db, "cursor", "0"); err != nil {
				return r, err
			}
		}
	}
	if err := pull(ctx, c, p, db, ws, r, cs); err != nil {
		return r, err
	}
	if scope != "workspace" {
		if err := setState(db, "artifact_scope", "workspace"); err != nil {
			return r, err
		}
	}
	if cs.ok && cardScope != "workspace" {
		if err := setState(db, "cards_scope", "workspace"); err != nil {
			return r, err
		}
	}
	return r, nil
}

// ------------------------------------------------------------------ push

func push(ctx context.Context, c *api.Client, p *project.Project, db *sql.DB, ws string, r *Report, cs *cardSync) error {
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
	if err := pruneRetries(db); err != nil {
		return err
	}

	// Desk items: anything created or closed since the last push.
	// assignedTo always goes up ("" clears it on the API); author never does:
	// the API sets it from the token (GRS-35).
	rows, err := db.Query(`SELECT id, type, from_agent, to_agent, text, ref, created_at, state, closed_at, answer, updated_at, assigned_to FROM desk_items
		WHERE created_at >= ? OR closed_at >= ? OR updated_at >= ?`+retrying("desk_items", "id"), since, since, since)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, typ, from, to, text, ref, created, state, closed, answer, updated, assigned string
		if err := rows.Scan(&id, &typ, &from, &to, &text, &ref, &created, &state, &closed, &answer, &updated, &assigned); err != nil {
			rows.Close()
			return err
		}
		row := map[string]any{"id": id, "type": typ, "from": from, "to": to, "text": text, "ref": ref, "state": state, "answer": answer, "createdAt": created,
			"updatedAt": latest(created, closed, updated), "assignedTo": assigned}
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
	rows, err = db.Query(`SELECT uid, at, agent, kind, text, project_id FROM session_entries WHERE at >= ?`+retrying("session_entries", "uid"), since)
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
	// Content (GRS-20): every artifact whose file differs from the body the
	// API last took, whatever its row's age, so a failed upload is retried
	// on each sync until it lands.
	var contentFor []contentUp
	uploaded, err := contentHashes(db)
	if err != nil {
		return err
	}
	for _, a := range arts {
		if a.FilePresent {
			if b, err := os.ReadFile(a.Path); err == nil {
				if h := artifact.Hash(string(b)); h != uploaded[a.ID] {
					contentFor = append(contentFor, contentUp{a: a, body: b, hash: h})
				}
			}
		}
	}
	retry, err := retrySet(db, "artifacts")
	if err != nil {
		return err
	}
	for _, a := range arts {
		row := artifactPushRow(a)
		if since != "" && row["updatedAt"].(string) < since && !retry[a.ID] {
			continue
		}
		in.Rows["artifacts"] = append(in.Rows["artifacts"], row)
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
		if isCardTable(tbl) && !cs.ok {
			continue // kept until the API has card tables
		}
		if in.Deletes == nil {
			in.Deletes = map[string][]string{}
		}
		in.Deletes[tbl] = append(in.Deletes[tbl], id)
		nDeletes++
	}
	tombs.Close()

	// The board, in the API's push order, when the API has card tables.
	if cs.ok {
		if err := collectCards(db, cs, &in); err != nil {
			return fmt.Errorf("cards: %w", err)
		}
	}

	failedRow := map[string]bool{}
	total := len(in.Rows["desk_items"]) + len(in.Rows["session_entries"]) + len(in.Rows["artifacts"]) + nDeletes + cs.sent
	if total > 0 {
		out, err := c.Push(ctx, ws, in)
		if err != nil {
			return fmt.Errorf("push: %w", err)
		}
		if cs.ok {
			answered, err := applyCardResults(db, cs, out.Results, r)
			if err != nil {
				return fmt.Errorf("cards: %w", err)
			}
			// Card rows sent and not one answered: an API that ignores the
			// tables. Same as no board route: keep the watermark.
			if cs.sent > 0 && !answered {
				cs.ok = false
				r.Cards = "skipped"
				r.Notes = append(r.Notes, api.ErrNoCards.Error()+" (cards stay local until it is)")
			}
		}
		r.Pushed, r.Skipped, r.Failed = out.Applied, out.Skipped, out.Failed
		if err := trackRetries(db, in, out.Results); err != nil {
			return err
		}
		failed := failedRow
		for _, res := range out.Results {
			if res.Error != "" {
				failed[res.Table+"/"+res.ID] = true
				r.Notes = append(r.Notes, fmt.Sprintf("push %s %s: %s", res.Table, short(res.ID), res.Error))
			}
		}
		for tbl, ids := range in.Deletes {
			for _, id := range ids {
				if failed[tbl+"/"+id] || (isCardTable(tbl) && !cs.ok) {
					continue
				}
				_, _ = db.Exec(`DELETE FROM sync_deletes WHERE tbl = ? AND id = ?`, tbl, id)
			}
		}
	}
	// Content after rows, so the row exists server-side.
	// A failure leaves content_hash as it was: the next sync tries again.
	for _, u := range contentFor {
		a := u.a
		if failedRow["artifacts/"+a.ID] {
			continue // no row on the API to hang the body on; retried next sync
		}
		fileID, ok, err := c.PutArtifactContent(ctx, a.ID, u.body)
		if api.IsNotFound(err) {
			// The API has no row for it: refused, or pushed by a CLI whose
			// watermark then passed it by (GRS-47). The row goes up again,
			// then the body.
			if perr := pushArtifactRow(ctx, c, db, ws, a, r); perr != nil {
				r.Notes = append(r.Notes, "content "+a.File+": the API has no row for it, and "+perr.Error()+" (retried on the next sync)")
				continue
			}
			fileID, ok, err = c.PutArtifactContent(ctx, a.ID, u.body)
		}
		if err != nil {
			r.Notes = append(r.Notes, "content "+a.File+": "+err.Error()+" (retried on the next sync)")
			continue
		}
		if !ok {
			r.Notes = append(r.Notes, "content not uploaded: server has no file store configured")
			break
		}
		r.ContentUp++
		if fileID != "" {
			_, _ = db.Exec(`UPDATE artifacts SET remote_file_id = ?, content_hash = ? WHERE id = ?`, fileID, u.hash, a.ID)
		} else {
			_, _ = db.Exec(`UPDATE artifacts SET content_hash = ? WHERE id = ?`, u.hash, a.ID)
		}
	}
	if cs.ok {
		if err := setState(db, "cards_pushed_at", cs.now); err != nil {
			return err
		}
	}
	return setState(db, "pushed_at", now)
}

// ------------------------------------------------------------------ pull

func pull(ctx context.Context, c *api.Client, p *project.Project, db *sql.DB, ws string, r *Report, cs *cardSync) error {
	cursorStr, _ := stateDB(db, "cursor")
	var cursor int64
	fmt.Sscan(cursorStr, &cursor)
	for {
		out, err := c.Pull(ctx, ws, cursor)
		if err != nil {
			return fmt.Errorf("pull: %w", err)
		}
		sortForApply(out.Changes)
		for _, ch := range out.Changes {
			if isCardTable(ch.Table) && !cs.ok {
				continue
			}
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
			break
		}
	}
	// Every card change up to the main cursor is applied now, so the card
	// stream (GRS-51) may start there; it never goes back.
	if cs.ok {
		return raiseCardsCursor(db, cursor)
	}
	return nil
}

func apply(ctx context.Context, c *api.Client, p *project.Project, db *sql.DB, ch api.Change, r *Report) (bool, error) {
	switch ch.Table {
	case "desk_items":
		if ch.Deleted {
			_, err := db.Exec(`DELETE FROM desk_items WHERE id = ?`, ch.ID)
			return true, err
		}
		var d struct {
			ID, Type, From, To, Text, Ref, State, Answer, Author string
			AssignedTo                                           *string
			CreatedAt, UpdatedAt                                 time.Time
			ClosedAt                                             *time.Time
		}
		if err := json.Unmarshal(ch.Row, &d); err != nil {
			return false, err
		}
		closed := ""
		if d.ClosedAt != nil {
			closed = d.ClosedAt.UTC().Format(time.RFC3339)
		}
		assigned := ""
		if d.AssignedTo != nil {
			assigned = *d.AssignedTo
		}
		_, err := db.Exec(`INSERT INTO desk_items (id, type, from_agent, to_agent, text, ref, created_at, state, closed_at, closed_by, answer, updated_at, author, assigned_to)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET type = excluded.type, from_agent = excluded.from_agent, to_agent = excluded.to_agent, text = excluded.text,
				ref = excluded.ref, state = excluded.state, closed_at = excluded.closed_at, answer = excluded.answer, updated_at = excluded.updated_at,
				author = excluded.author, assigned_to = excluded.assigned_to`,
			d.ID, d.Type, d.From, d.To, d.Text, d.Ref, d.CreatedAt.UTC().Format(time.RFC3339), d.State, closed, d.Answer, d.UpdatedAt.UTC().Format(time.RFC3339),
			d.Author, assigned)
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
		if a.Prefix == "arch" && pid == "" {
			if err := dropStrayArch(db, p, a.ID, r); err != nil {
				return false, err
			}
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
					// The API holds this body: nothing to upload back.
					_, _ = db.Exec(`UPDATE artifacts SET file_present = 1, content_hash = ? WHERE id = ?`, artifact.Hash(string(body)), a.ID)
				}
			}
		}
		// No body to fetch (no file store, or none uploaded) and the cache
		// holds a different one: keep the row on the local hash, so
		// Reconcile does not read the file as an edit and push it over the
		// server's row. A missing file is regenerated the same way.
		// The API has a body and it is the one here: nothing to upload.
		if a.FileID != "" && !fetched && localHash != "" && localHash == a.BodyHash {
			_, _ = db.Exec(`UPDATE artifacts SET content_hash = ? WHERE id = ?`, localHash, a.ID)
		}
		if present && !fetched && localHash != "" && localHash != a.BodyHash {
			_, _ = db.Exec(`UPDATE artifacts SET body_hash = ? WHERE id = ?`, localHash, a.ID)
		}
		return true, nil
	case "workspaces", "projects":
		// Binding is local; nothing to apply.
		return false, nil
	}
	if isCardTable(ch.Table) {
		return applyCard(db, ch)
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

// contentUp is one artifact body to upload, read once with its hash.
type contentUp struct {
	a    artifact.Artifact
	body []byte
	hash string
}

// contentHashes maps artifact id to the hash of the body the API last took
// or gave (GRS-20); "" when it has none.
func contentHashes(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT id, content_hash FROM artifacts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, h string
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		out[id] = h
	}
	return out, rows.Err()
}

// ------------------------------------------------------------- retries

// artifactPushRow is an artifact as it goes up in a push.
func artifactPushRow(a artifact.Artifact) map[string]any {
	row := map[string]any{"id": a.ID, "prefix": a.Prefix, "slug": a.Slug, "title": a.Title, "status": a.Status, "tags": a.Tags,
		"bodyHash": a.BodyHash, "shapeOk": a.ShapeOK, "updatedAt": a.UpdatedAt.UTC().Format(time.RFC3339)}
	// The row's own project, never the binding's: one cache holds every
	// project's artifacts and the workspace-level ones (GRS-40).
	if a.ProjectID != "" {
		row["projectId"] = a.ProjectID
	}
	return row
}

// pushArtifactRow pushes one artifact row on its own: the API answered its
// content with 404, so it has no row for it.
func pushArtifactRow(ctx context.Context, c *api.Client, db *sql.DB, ws string, a artifact.Artifact, r *Report) error {
	in := api.PushIn{Rows: map[string][]any{"artifacts": {artifactPushRow(a)}}}
	out, err := c.Push(ctx, ws, in)
	if err != nil {
		return fmt.Errorf("push: %w", err)
	}
	if err := trackRetries(db, in, out.Results); err != nil {
		return err
	}
	for _, res := range out.Results {
		if res.Table == "artifacts" && res.ID == a.ID && res.Error != "" {
			r.Failed++
			return fmt.Errorf("push of its row: %s", res.Error)
		}
	}
	r.Pushed += out.Applied
	return nil
}

// retryKey is the column each table's pushed id comes from.
var retryKey = map[string]string{
	"desk_items": "id", "session_entries": "uid", "artifacts": "id",
	"card_states": "id", "cards": "id", "card_comments": "id", "card_links": "id",
	"card_checklists": "id", "card_checklist_items": "id", "card_events": "uid",
}

// pruneRetries forgets refused rows that are gone here (deleted, or a pull
// took them away): there is nothing left to push.
func pruneRetries(db *sql.DB) error {
	for tbl, col := range retryKey {
		if _, err := db.Exec(`DELETE FROM sync_retry WHERE tbl = ? AND id NOT IN (SELECT `+col+` FROM `+tbl+`)`, tbl); err != nil {
			return err
		}
	}
	return nil
}

// dropStrayArch makes way for the workspace's architecture doc (one per
// workspace, GRS-41) when the API sends it: a cache that wrote its own stub
// before it knew the workspace (gears init in a standalone clone) drops that
// stub, which the API refuses as a second arch, so the workspace's takes
// arch--primary.md. A local arch that was edited is kept (cached beside;
// its refusal stays in the sync notes for someone to settle).
func dropStrayArch(db *sql.DB, p *project.Project, keepID string, r *Report) error {
	type stray struct{ id, file, title string }
	var found []stray
	err := scanAll(db, `SELECT id, file, title FROM artifacts WHERE prefix = 'arch' AND project_id = '' AND id != ?`, []any{keepID},
		func(sc func(...any) error) error {
			var s stray
			if err := sc(&s.id, &s.file, &s.title); err != nil {
				return err
			}
			found = append(found, s)
			return nil
		})
	if err != nil {
		return err
	}
	for _, s := range found {
		b, err := os.ReadFile(filepath.Join(p.ArtifactsDir(), s.file))
		if err == nil && artifact.Hash(string(b)) != artifact.Hash(artifact.ArchStub(s.title)) {
			continue
		}
		if err := artifact.Forget(db, p, s.id); err != nil {
			return err
		}
		_, _ = db.Exec(`DELETE FROM sync_retry WHERE tbl = 'artifacts' AND id = ?`, s.id)
		r.Notes = append(r.Notes, fmt.Sprintf("artifact %s: an unedited local architecture stub gave way to the workspace's", short(s.id)))
	}
	return nil
}

// retrying is the SQL that adds a table's refused rows to a "changed since"
// query: col is the column the pushed id comes from.
func retrying(tbl, col string) string {
	return " OR " + col + " IN (SELECT id FROM sync_retry WHERE tbl = '" + tbl + "')"
}

// retrySet is the ids of one table's refused rows.
func retrySet(db *sql.DB, tbl string) (map[string]bool, error) {
	out := map[string]bool{}
	err := scanAll(db, `SELECT id FROM sync_retry WHERE tbl = ?`, []any{tbl}, func(sc func(...any) error) error {
		var id string
		if err := sc(&id); err != nil {
			return err
		}
		out[id] = true
		return nil
	})
	return out, err
}

// trackRetries keeps sync_retry in step with what the API answered: every
// row sent and refused is kept (and goes up on every push until it lands),
// every row sent and taken is cleared. Deletes have their own queue
// (sync_deletes) and are not tracked here.
func trackRetries(db *sql.DB, in api.PushIn, results []api.PushResult) error {
	refused := map[string]string{}
	for _, res := range results {
		if res.Error != "" {
			refused[res.Table+"/"+res.ID] = res.Error
		}
	}
	now := store.Now()
	return inTx(db, func(tx *sql.Tx) error {
		for tbl, rows := range in.Rows {
			for _, row := range rows {
				m, ok := row.(map[string]any)
				if !ok {
					continue
				}
				id, _ := m["id"].(string)
				if id == "" {
					continue
				}
				if msg, bad := refused[tbl+"/"+id]; bad {
					if _, err := tx.Exec(`INSERT INTO sync_retry (tbl, id, error, at) VALUES (?, ?, ?, ?)
						ON CONFLICT(tbl, id) DO UPDATE SET error = excluded.error, at = excluded.at`, tbl, id, msg, now); err != nil {
						return err
					}
					continue
				}
				if _, err := tx.Exec(`DELETE FROM sync_retry WHERE tbl = ? AND id = ?`, tbl, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
