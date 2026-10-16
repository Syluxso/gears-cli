package sync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/gitx"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/store"
)

// fakeAPI answers push and pull like gears-api: a delete for an id it never
// saw is skipped, pulls return whatever the test queued, bind finds a
// project by normalised remote, content is stored by artifact id.
type fakeAPI struct {
	mu       stdsync.Mutex
	pushes   []api.PushIn
	pending  []api.Change
	projects map[string]api.Project
	projGets int
	projUps  []api.Project
	ws       string            // the workspace bind reports
	content  map[string][]byte // artifact id -> body
	cursors  []string          // cursor asked for on each pull
	board    fakeBoard

	// GRS-47: refusePrefix answers an artifact row of that prefix with an
	// error (as gears-api did for arch before de37b4d). With strictContent,
	// a content upload for an artifact the fake has no row for is 404.
	refusePrefix  string
	strictContent bool
	artifactRows  map[string]bool

	// deskVisible answers pulls as an API with desk privacy (GRS-48).
	deskVisible bool
	// gone answers push and pull 410 gears.workspace_deleted (GRS-80).
	gone bool
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone && strings.HasSuffix(r.URL.Path, "/sync") {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusGone)
		_, _ = io.WriteString(w, `{"status":410,"code":"gears.workspace_deleted","detail":"workspace synctest was deleted"}`)
		return
	}
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync"):
		var in api.PushIn
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		f.pushes = append(f.pushes, in)
		out := api.PushOut{Results: []api.PushResult{}}
		// The API's order: parents first, then the board in push order.
		order := []string{"workspaces", "projects", "desk_items", "session_entries", "artifacts"}
		order = append(order, cardTables...)
		for _, tbl := range order {
			if isCardTable(tbl) && (f.board.noCards || f.board.ignoreTables) {
				continue // an API from before GRS-47 ignores the tables
			}
			for _, row := range in.Rows[tbl] {
				m := row.(map[string]any)
				id := m["id"].(string)
				res := api.PushResult{Table: tbl, ID: id, Applied: true}
				if isCardTable(tbl) {
					res = f.board.push(tbl, m)
				}
				if tbl == "artifacts" {
					if f.refusePrefix != "" && m["prefix"] == f.refusePrefix {
						res = api.PushResult{Table: tbl, ID: id, Error: "artifacts: invalid prefix"}
						out.Failed++
						out.Results = append(out.Results, res)
						continue
					}
					if f.artifactRows == nil {
						f.artifactRows = map[string]bool{}
					}
					f.artifactRows[id] = true
				}
				out.Results = append(out.Results, res)
				if res.Applied {
					out.Applied++
				} else {
					out.Skipped++
				}
			}
		}
		for tbl, ids := range in.Deletes {
			if isCardTable(tbl) && (f.board.noCards || f.board.ignoreTables) {
				continue
			}
			for _, id := range ids {
				out.Results = append(out.Results, api.PushResult{Table: tbl, ID: id})
				out.Skipped++
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/board"):
		if f.board.noCards {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(f.board.get())
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sync"):
		f.cursors = append(f.cursors, r.URL.Query().Get("cursor"))
		out := api.PullOut{Changes: f.pending}
		if out.Changes == nil {
			out.Changes = []api.Change{}
		}
		out.Cursor = int64(len(f.pending))
		if f.deskVisible {
			out.Desk = "visible"
		}
		f.pending = nil
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects":
		var pr api.Project
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &pr)
		f.projUps = append(f.projUps, pr)
		if f.projects == nil {
			f.projects = map[string]api.Project{}
		}
		f.projects[pr.ID] = pr
		_ = json.NewEncoder(w).Encode(pr)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/bind":
		want := gitx.NormalizeRemote(r.URL.Query().Get("remote"))
		for _, pr := range f.projects {
			if gitx.NormalizeRemote(pr.GitRemote) == want {
				_ = json.NewEncoder(w).Encode(api.BindOut{Project: pr, Workspace: api.Workspace{ID: f.ws, Slug: "ws"}})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(api.Problem{Status: http.StatusNotFound, Code: "not_found"})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/projects/"):
		f.projGets++
		pr, ok := f.projects[strings.TrimPrefix(r.URL.Path, "/api/v1/projects/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(api.Problem{Status: http.StatusNotFound, Code: "not_found"})
			return
		}
		_ = json.NewEncoder(w).Encode(pr)
	case strings.HasSuffix(r.URL.Path, "/content"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/artifacts/"), "/content")
		if r.Method == http.MethodPut {
			if f.strictContent && !f.artifactRows[id] {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"title":"Not Found","status":404,"detail":"not found","code":"gears.not_found"}`)
				return
			}
			b, _ := io.ReadAll(r.Body)
			if f.content == nil {
				f.content = map[string][]byte{}
			}
			f.content[id] = b
			_ = json.NewEncoder(w).Encode(map[string]string{"fileId": "f-" + id})
			return
		}
		b, ok := f.content[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

// A local delete is pushed as a delete and its tombstone cleared; a pulled
// delete removes the row and the file, and reconcile does not re-adopt it.
func TestSyncDeletes(t *testing.T) {
	dir := t.TempDir()
	res, err := project.Init(dir, "synctest", "ST")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID

	gone, err := artifact.New(p, "free", "Gone offline", "tester")
	if err != nil {
		t.Fatal(err)
	}
	kept, err := artifact.New(p, "idea", "Deleted elsewhere", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.Delete(p, gone.ID[len(gone.ID)-8:]); err != nil {
		t.Fatal(err)
	}

	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)

	// Push carries the delete; the skipped (never pushed) delete still clears.
	if _, err := Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	if len(f.pushes) != 1 {
		t.Fatalf("pushes: %d", len(f.pushes))
	}
	if ids := f.pushes[0].Deletes["artifacts"]; len(ids) != 1 || ids[0] != gone.ID {
		t.Fatalf("push deletes: %v", f.pushes[0].Deletes)
	}
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	var tomb int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sync_deletes`).Scan(&tomb)
	db.Close()
	if tomb != 0 {
		t.Fatalf("tombstones left after push: %d", tomb)
	}

	// Another machine deleted kept; the pull drops row and file.
	f.mu.Lock()
	f.pending = []api.Change{{Seq: 1, Table: "artifacts", ID: kept.ID, Op: "delete", Deleted: true}}
	f.mu.Unlock()
	r, err := Run(context.Background(), cl, p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Applied != 1 {
		t.Fatalf("applied: %d (notes %v)", r.Applied, r.Notes)
	}
	if _, err := os.Stat(filepath.Join(p.ArtifactsDir(), kept.File)); !os.IsNotExist(err) {
		t.Fatalf("pulled delete left the file: %v", err)
	}
	changes, err := artifact.Reconcile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("reconcile after pulled delete: %v", changes)
	}
	list, err := artifact.List(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("artifacts left: %v", list)
	}
}

// pushedChanges turns every artifact row pushed so far into pull changes, as
// gears-api would hand them to any cache in the workspace.
func (f *fakeAPI) pushedChanges(t *testing.T) []api.Change {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []api.Change
	for _, in := range f.pushes {
		for _, row := range in.Rows["artifacts"] {
			b, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			id := row.(map[string]any)["id"].(string)
			out = append(out, api.Change{Seq: int64(len(out) + 1), Table: "artifacts", ID: id, Op: "upsert", Row: b})
		}
	}
	return out
}

func (f *fakeAPI) queue(ch []api.Change) {
	f.mu.Lock()
	f.pending = ch
	f.mu.Unlock()
}

func (f *fakeAPI) counts() (gets int, ups []api.Project) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.projGets, append([]api.Project{}, f.projUps...)
}

func gitInit(t *testing.T, dir, remote string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	args := [][]string{{"init", "-q"}}
	if remote != "" {
		args = append(args, []string{"remote", "add", "origin", remote})
	}
	for _, a := range args {
		c := exec.Command("git", a...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
}

func artifactRow(t *testing.T, id, prefix, slug, projectID, body string) api.Change {
	t.Helper()
	row := map[string]any{"id": id, "prefix": prefix, "slug": slug, "title": slug, "status": "draft", "tags": []string{},
		"bodyHash": artifact.Hash(body), "shapeOk": true, "fileId": "f-" + id,
		"createdAt": "2026-10-12T10:00:00Z", "updatedAt": "2026-10-12T10:00:00Z", "createdBy": "elsewhere"}
	if projectID != "" {
		row["projectId"] = projectID
	}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return api.Change{Seq: 1, Table: "artifacts", ID: id, Op: "upsert", Row: b}
}

// GRS-40: the workspace root's sync makes sure every non-missing manifest
// project exists in the account: one GET each, create when absent, refresh
// a moved remote, adopt the API's id for a remote scanned offline (retagging
// the cache's rows before they are pushed), skip missing entries, and never
// make the workspace itself a project. A re-run writes nothing.
func TestSyncEnsuresManifestProjects(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	res, err := project.Init(dir, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	root := res.Project
	root.Workspace = root.ID
	gitInit(t, filepath.Join(dir, "projects", "alpha"), "git@github.com:example/alpha.git")
	gitInit(t, filepath.Join(dir, "projects", "beta"), "") // no remote, no commits
	gitInit(t, filepath.Join(dir, "projects", "late"), "https://github.com/example/late.git")
	const alphaID = "01a12688-563c-7dda-a360-db460f196103"
	const lateAPI = "01a133a5-57f7-7c78-827b-ac2716ee35ba"
	m := &project.Manifest{Projects: []project.ManifestEntry{
		{ID: alphaID, Slug: "alpha", Path: "projects/alpha", Remote: "git@github.com:example/alpha.git"},
		{ID: "01a1bbbb-0000-7000-8000-00000000beta", Slug: "beta", Path: "projects/beta"},
		{ID: "01a1cccc-0000-7000-8000-0000minted00", Slug: "late", Path: "projects/late", Remote: "https://github.com/example/late.git"},
		{ID: "01a1dddd-0000-7000-8000-000000000gone", Slug: "gone", Path: "projects/gone", Missing: true},
	}}
	if err := m.Save(root); err != nil {
		t.Fatal(err)
	}
	// Work logged against the minted id before the first sync.
	lateDir := filepath.Join(dir, "projects", "late")
	if _, err := session.AppendFor(root, project.Current(root, lateDir).ID, "done", "tester", "late work"); err != nil {
		t.Fatal(err)
	}

	f := &fakeAPI{projects: map[string]api.Project{
		alphaID: {ID: alphaID, WorkspaceID: root.ID, Slug: "alpha", GitRemote: "github.com/example/alpha"},
		lateAPI: {ID: lateAPI, WorkspaceID: root.ID, Slug: "late", GitRemote: "github.com/example/late"},
	}, ws: root.ID}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	ctx := context.Background()

	r, err := Run(ctx, cl, root)
	if err != nil {
		t.Fatal(err)
	}
	gets, ups := f.counts()
	// One GET each: alpha, beta, late (missed by id, found by remote).
	if r.Projects != 3 || r.ProjectsCreated != 1 || gets != 3 {
		t.Fatalf("checked %d created %d gets %d notes %v", r.Projects, r.ProjectsCreated, gets, r.Notes)
	}
	if len(ups) != 1 || ups[0].ID != "01a1bbbb-0000-7000-8000-00000000beta" || ups[0].WorkspaceID != root.ID || ups[0].Slug != "beta" || ups[0].GitRemote != "" {
		t.Fatalf("upserts: %+v", ups)
	}
	for _, u := range ups {
		if u.ID == root.ID {
			t.Fatal("workspace upserted as a project")
		}
	}
	m, _ = project.LoadManifest(root)
	if e := m.Lookup("late"); e == nil || e.ID != lateAPI {
		t.Fatalf("late not adopted: %+v", m.Projects)
	}
	if e := m.Lookup("gone"); e == nil || !e.Missing {
		t.Fatalf("missing entry dropped or revived: %+v", m.Projects)
	}
	// The entry logged under the minted id went up under the API's id.
	f.mu.Lock()
	pushed := f.pushes[0].Rows["session_entries"]
	f.mu.Unlock()
	if len(pushed) != 1 || pushed[0].(map[string]any)["projectId"] != lateAPI {
		t.Fatalf("pushed session entries: %v", pushed)
	}

	// Re-run: one GET each, nothing written.
	if _, err := Run(ctx, cl, root); err != nil {
		t.Fatal(err)
	}
	gets2, ups2 := f.counts()
	if gets2-gets != 3 || len(ups2) != 1 {
		t.Fatalf("re-run: gets %d upserts %+v", gets2-gets, ups2)
	}

	// alpha's remote moved: refreshed, keeping the API's slug and name.
	f.mu.Lock()
	f.projects[alphaID] = api.Project{ID: alphaID, WorkspaceID: root.ID, Slug: "alpha-renamed", Name: "Alpha", GitRemote: "github.com/example/alpha"}
	f.mu.Unlock()
	c := exec.Command("git", "remote", "set-url", "origin", "https://github.com/example/alpha-moved.git")
	c.Dir = filepath.Join(dir, "projects", "alpha")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := Run(ctx, cl, root); err != nil {
		t.Fatal(err)
	}
	_, ups3 := f.counts()
	if len(ups3) != 2 {
		t.Fatalf("remote refresh upserts: %+v", ups3)
	}
	if up := ups3[1]; up.ID != alphaID || up.GitRemote != "https://github.com/example/alpha-moved.git" || up.Slug != "alpha-renamed" || up.Name != "Alpha" {
		t.Fatalf("refreshed: %+v", up)
	}
	m, _ = project.LoadManifest(root)
	if e := m.Lookup("alpha"); e.Remote != "https://github.com/example/alpha-moved.git" {
		t.Fatalf("manifest remote not refreshed: %+v", e)
	}
}

// GRS-40: the root's one cache holds every artifact of the workspace,
// tagged with its project. Pushes carry each row's own projectId. A row from
// elsewhere whose file name is taken here is cached under a suffixed name,
// keeps its server slug, and neither file is touched; repeat pulls are quiet.
func TestSyncWorkspaceCacheHoldsAllArtifacts(t *testing.T) {
	dir := t.TempDir()
	res, err := project.Init(dir, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	root := res.Project
	root.Workspace = root.ID
	const alphaID = "01a12688-563c-7dda-a360-db460f196103"
	m := &project.Manifest{Projects: []project.ManifestEntry{{ID: alphaID, Slug: "alpha", Path: "projects/alpha"}}}
	if err := m.Save(root); err != nil {
		t.Fatal(err)
	}
	wsArt, err := artifact.New(root, "free", "Same name", "tester")
	if err != nil {
		t.Fatal(err)
	}
	alphaArt, err := artifact.NewFor(root, alphaID, "idea", "Alpha idea", "tester")
	if err != nil {
		t.Fatal(err)
	}
	rootBody := "root body\n"
	if err := os.WriteFile(wsArt.Path, []byte(rootBody), 0o644); err != nil {
		t.Fatal(err)
	}

	f := &fakeAPI{projects: map[string]api.Project{alphaID: {ID: alphaID, WorkspaceID: root.ID, Slug: "alpha"}}, ws: root.ID}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	ctx := context.Background()
	if _, err := Run(ctx, cl, root); err != nil {
		t.Fatal(err)
	}
	for _, ch := range f.pushedChanges(t) {
		var row map[string]any
		_ = json.Unmarshal(ch.Row, &row)
		pid, has := row["projectId"]
		switch ch.ID {
		case wsArt.ID:
			if has {
				t.Fatalf("workspace artifact pushed with projectId %v", pid)
			}
		case alphaArt.ID:
			if pid != alphaID {
				t.Fatalf("alpha artifact pushed with projectId %v", pid)
			}
		}
	}

	// alpha's own free--same-name.md, made in a pre-GRS-40 project binding.
	const otherID = "01a130c3-0000-7000-8000-0000173b938f"
	alphaBody := "alpha body\n"
	f.mu.Lock()
	f.content = map[string][]byte{otherID: []byte(alphaBody)}
	f.mu.Unlock()
	pulled := artifactRow(t, otherID, "free", "same-name", alphaID, alphaBody)
	for round := 0; round < 2; round++ {
		f.queue([]api.Change{pulled})
		r, err := Run(ctx, cl, root)
		if err != nil {
			t.Fatal(err)
		}
		if r.Applied != 1 || r.Failed != 0 {
			t.Fatalf("round %d: applied %d failed %d notes %v", round, r.Applied, r.Failed, r.Notes)
		}
		wantNotes := 0
		if round == 0 {
			wantNotes = 1
		}
		if len(r.Notes) != wantNotes || (wantNotes == 1 && !strings.Contains(r.Notes[0], "free--same-name--alpha.md")) {
			t.Fatalf("round %d notes: %v", round, r.Notes)
		}
	}
	list, err := artifact.List(root)
	if err != nil {
		t.Fatal(err)
	}
	byFile := map[string]artifact.Artifact{}
	for _, a := range list {
		byFile[a.File] = a
	}
	if len(list) != 3 {
		t.Fatalf("cache: %+v", list)
	}
	if a := byFile["free--same-name.md"]; a.ID != wsArt.ID || a.ProjectID != "" {
		t.Fatalf("workspace artifact: %+v", a)
	}
	if a := byFile["free--same-name--alpha.md"]; a.ID != otherID || a.ProjectID != alphaID || a.Slug != "same-name" {
		t.Fatalf("alpha's copy: %+v", a)
	}
	if got, _ := os.ReadFile(wsArt.Path); string(got) != rootBody {
		t.Fatalf("workspace file changed: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root.ArtifactsDir(), "free--same-name--alpha.md")); string(got) != alphaBody {
		t.Fatalf("alpha file: %q", got)
	}

	// Edit alpha's copy here: it goes up under its server slug and project.
	if err := os.WriteFile(filepath.Join(root.ArtifactsDir(), "free--same-name--alpha.md"), []byte(alphaBody+"more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // updated_at is to the second
	f.mu.Lock()
	f.pushes = nil
	f.mu.Unlock()
	if _, err := Run(ctx, cl, root); err != nil {
		t.Fatal(err)
	}
	var up map[string]any
	for _, ch := range f.pushedChanges(t) {
		if ch.ID == otherID {
			_ = json.Unmarshal(ch.Row, &up)
		}
	}
	if up == nil || up["slug"] != "same-name" || up["projectId"] != alphaID {
		t.Fatalf("pushed edit: %v", up)
	}
}

// GRS-40: a cache whose cursor ran under the GRS-26 rule (skipping other
// bindings' artifacts) pulls the workspace from the start once, then
// carries on from the server's cursor.
func TestSyncRepullsOnce(t *testing.T) {
	res, err := project.Init(t.TempDir(), "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := setState(db, "cursor", "86"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	for i := 0; i < 2; i++ {
		if _, err := Run(context.Background(), cl, p); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cursors) != 2 || f.cursors[0] != "0" || f.cursors[1] == "86" {
		t.Fatalf("pull cursors: %v", f.cursors)
	}
}

// A standalone clone (bound by remote, no workspace above it) still works:
// it makes sure of its own project row, its artifacts carry its id, and a
// workspace-level artifact pulled into it is not re-homed when edited.
func TestSyncStandaloneClone(t *testing.T) {
	res, err := project.Init(t.TempDir(), "alpha", "AL")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = "01a12688-57c3-7a3c-9fe1-0698ffeb676b" // bound by remote
	if p.IsWorkspace() {
		t.Fatal("standalone clone reads as a workspace")
	}
	own, err := artifact.New(p, "free", "Clone note", "tester")
	if err != nil || own.ProjectID != p.ID {
		t.Fatalf("own artifact: %+v %v", own, err)
	}
	f := &fakeAPI{ws: p.Workspace}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	ctx := context.Background()
	f.queue([]api.Change{artifactRow(t, "01a130c3-eaa4-745f-8f12-30b017b2aa10", "runbook", "single-ticket-development", "", "steps: []\n")})
	f.content = map[string][]byte{"01a130c3-eaa4-745f-8f12-30b017b2aa10": []byte("steps: []\n")}
	if _, err := Run(ctx, cl, p); err != nil {
		t.Fatal(err)
	}
	_, ups := f.counts()
	if len(ups) != 1 || ups[0].ID != p.ID || ups[0].WorkspaceID != p.Workspace {
		t.Fatalf("project upserts: %+v", ups)
	}
	var pid any = "unset"
	for _, ch := range f.pushedChanges(t) {
		if ch.ID == own.ID {
			var row map[string]any
			_ = json.Unmarshal(ch.Row, &row)
			pid = row["projectId"]
		}
	}
	if pid != p.ID {
		t.Fatalf("clone artifact pushed with projectId %v", pid)
	}
	ws, err := artifact.Find(p, "runbook--single-ticket-development.yaml")
	if err != nil || ws.ProjectID != "" {
		t.Fatalf("workspace runbook in clone: %+v %v", ws, err)
	}
	if err := os.WriteFile(ws.Path, []byte("steps: [] # edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	f.mu.Lock()
	f.pushes = nil
	f.mu.Unlock()
	if _, err := Run(ctx, cl, p); err != nil {
		t.Fatal(err)
	}
	for _, ch := range f.pushedChanges(t) {
		if ch.ID == ws.ID {
			var row map[string]any
			_ = json.Unmarshal(ch.Row, &row)
			if _, has := row["projectId"]; has {
				t.Fatalf("workspace runbook re-homed: %v", row)
			}
			return
		}
	}
	t.Fatal("edited runbook not pushed")
}

// Rows written in the same second as the last push (timestamps are to the
// second) still go up on the next sync. With one cache per workspace, a
// write right after any sync from anywhere in the workspace hits this.
func TestSyncPushesRowsFromTheLastPushSecond(t *testing.T) {
	res, err := project.Init(t.TempDir(), "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID
	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	for {
		// Start early in a second so the writes below share the push's stamp.
		if time.Now().Nanosecond() < 500e6 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	a, err := artifact.New(p, "free", "Right after", "tester")
	if err != nil {
		t.Fatal(err)
	}
	e, err := session.Append(p, "done", "tester", "right after")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	f.mu.Lock()
	for _, in := range f.pushes {
		for _, tbl := range []string{"artifacts", "session_entries"} {
			for _, row := range in.Rows[tbl] {
				got[row.(map[string]any)["id"].(string)] = true
			}
		}
	}
	f.mu.Unlock()
	if !got[a.ID] || !got[e.ID] {
		t.Fatalf("rows from the last push's second not pushed: artifact %v entry %v", got[a.ID], got[e.ID])
	}
}

// A pulled artifact whose body cannot be fetched (no file store, nothing
// uploaded) gets a seeded stub in the cache, and the stub is never pushed
// back over the server's row. Before GRS-40 a cache rarely pulled rows it
// did not make; now the root pulls every project's.
func TestSyncDoesNotPushStubForBodylessRow(t *testing.T) {
	res, err := project.Init(t.TempDir(), "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID
	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	const id = "01a130c3-0000-7000-8000-0000173b938f"
	f.queue([]api.Change{artifactRow(t, id, "runbook", "single-ticket-development", "01a12688-563c-7dda-a360-db460f196103", "steps: [real]\n")})
	if _, err := Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	for i := 0; i < 2; i++ {
		if _, err := Run(context.Background(), cl, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, ch := range f.pushedChanges(t) {
		if ch.ID == id {
			t.Fatalf("stub pushed back: %s", ch.Row)
		}
	}
	a, err := artifact.Find(p, id)
	if err != nil || !a.FilePresent {
		t.Fatalf("cached row: %+v %v", a, err)
	}
}

// GRS-47: a row the API refused stays pending and goes up on every later
// sync, past the watermark, until the API takes it; its content follows.
func TestSyncRetriesRefusedRow(t *testing.T) {
	res, err := project.Init(t.TempDir(), "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID
	f := &fakeAPI{refusePrefix: "arch", strictContent: true}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	a, err := artifact.New(p, "arch", "Primary", "tester")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), cl, p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Failed != 1 || r.ContentUp != 0 {
		t.Fatalf("first sync: failed %d content %d notes %v", r.Failed, r.ContentUp, r.Notes)
	}
	time.Sleep(1100 * time.Millisecond) // the watermark passes the row
	// Still refused: still pending, still sent.
	if r, err = Run(context.Background(), cl, p); err != nil || r.Failed != 1 {
		t.Fatalf("second sync: %+v %v", r, err)
	}
	f.mu.Lock()
	f.refusePrefix = ""
	f.mu.Unlock()
	time.Sleep(1100 * time.Millisecond)
	if r, err = Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	if r.Failed != 0 || r.ContentUp != 1 {
		t.Fatalf("after the API takes arch: failed %d content %d notes %v", r.Failed, r.ContentUp, r.Notes)
	}
	f.mu.Lock()
	landed, body := f.artifactRows[a.ID], f.content[a.ID]
	f.mu.Unlock()
	if !landed || len(body) == 0 {
		t.Fatalf("row %v body %q", landed, body)
	}
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sync_retry`).Scan(&n)
	if n != 0 {
		t.Fatalf("sync_retry still holds %d rows", n)
	}
	// Nothing left to send.
	time.Sleep(1100 * time.Millisecond)
	if r, err = Run(context.Background(), cl, p); err != nil || r.ContentUp != 0 || len(r.Notes) != 0 {
		t.Fatalf("settled sync: %+v %v", r, err)
	}
}

// GRS-47: a cache whose watermark passed a refused row before retries were
// tracked (the arch--primary.md case): the content upload is 404, so the
// row goes up again and then the body.
func TestSyncContent404RepushesRow(t *testing.T) {
	res, err := project.Init(t.TempDir(), "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID
	f := &fakeAPI{refusePrefix: "arch", strictContent: true}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	a, err := artifact.New(p, "arch", "Primary", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	// As an older CLI left it: no record of the refusal.
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM sync_retry`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	f.mu.Lock()
	f.refusePrefix = ""
	f.mu.Unlock()
	time.Sleep(1100 * time.Millisecond)
	r, err := Run(context.Background(), cl, p)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	landed, body := f.artifactRows[a.ID], f.content[a.ID]
	f.mu.Unlock()
	if !landed || len(body) == 0 || r.ContentUp != 1 || r.Pushed != 1 {
		t.Fatalf("row %v body %q report %+v", landed, body, r)
	}
}

// A cache that wrote its own architecture stub before it knew the workspace
// (the API refuses it: one arch per workspace) drops the unedited stub when
// the workspace's arrives, and stops retrying it; an edited one is kept.
func TestSyncStrayArchStubGivesWay(t *testing.T) {
	for _, edited := range []bool{false, true} {
		res, err := project.Init(t.TempDir(), "ws", "WS")
		if err != nil {
			t.Fatal(err)
		}
		p := res.Project
		p.Workspace = p.ID
		f := &fakeAPI{refusePrefix: "arch"}
		srv := httptest.NewServer(f)
		local, _, err := artifact.InitArch(p, "tester")
		if err != nil {
			t.Fatal(err)
		}
		if edited {
			if err := os.WriteFile(local.Path, []byte("---\ntitle: Architecture\n---\n\n# Mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		const theirs = "01a130c3-0000-7000-8000-00000000a7c1"
		f.queue([]api.Change{artifactRow(t, theirs, "arch", "primary", "", "# Theirs\n")})
		cl := api.Anonymous(srv.URL)
		if _, err := Run(context.Background(), cl, p); err != nil {
			t.Fatal(err)
		}
		_, err = artifact.Find(p, local.ID)
		if gone := err != nil; gone == edited {
			t.Fatalf("edited %v: local arch gone %v", edited, gone)
		}
		th, err := artifact.Find(p, theirs)
		if err != nil {
			t.Fatal(err)
		}
		if !edited && th.File != "arch--primary.md" {
			t.Fatalf("workspace arch cached as %s", th.File)
		}
		r, err := Run(context.Background(), cl, p)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[bool]int{false: 0, true: 1}[edited]; r.Failed != want {
			t.Fatalf("edited %v: failed %d notes %v", edited, r.Failed, r.Notes)
		}
		srv.Close()
	}
}

// An empty body from the API never overwrites a body here, and an empty
// file is not uploaded (the API refuses one).
func TestSyncKeepsBodyOverEmptyAnswer(t *testing.T) {
	res, err := project.Init(t.TempDir(), "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID
	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	arch, _, err := artifact.InitArch(p, "tester")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(arch.Path)
	f.content = map[string][]byte{arch.ID: {}}
	f.queue([]api.Change{artifactRow(t, arch.ID, "arch", "primary", "", "something else\n")})
	r, err := Run(context.Background(), cl, p)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(arch.Path)
	if len(after) == 0 || string(after) != string(before) {
		t.Fatalf("local body replaced by an empty answer: %q (notes %v)", after, r.Notes)
	}
	if err := os.WriteFile(arch.Path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.Reconcile(p); err != nil {
		t.Fatal(err)
	}
	if r, err = Run(context.Background(), cl, p); err != nil || r.ContentUp != 0 {
		t.Fatalf("empty file uploaded: %+v %v", r, err)
	}
}
