package sync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
)

// fakeAPI answers push and pull like gears-api: a delete for an id it never
// saw is skipped, pulls return whatever the test queued.
type fakeAPI struct {
	mu      stdsync.Mutex
	pushes  []api.PushIn
	pending []api.Change
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync"):
		var in api.PushIn
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		f.pushes = append(f.pushes, in)
		out := api.PushOut{Results: []api.PushResult{}}
		for tbl, rows := range in.Rows {
			for _, row := range rows {
				id := row.(map[string]any)["id"].(string)
				out.Results = append(out.Results, api.PushResult{Table: tbl, ID: id, Applied: true})
				out.Applied++
			}
		}
		for tbl, ids := range in.Deletes {
			for _, id := range ids {
				out.Results = append(out.Results, api.PushResult{Table: tbl, ID: id})
				out.Skipped++
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sync"):
		out := api.PullOut{Changes: f.pending}
		if out.Changes == nil {
			out.Changes = []api.Change{}
		}
		out.Cursor = int64(len(f.pending))
		f.pending = nil
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasSuffix(r.URL.Path, "/content"):
		w.WriteHeader(http.StatusNotImplemented)
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

// GRS-26: the workspace root and a project inside it each have an artifact
// with the same file name. Each push carries its own binding (projectId for
// the project, none for the root); each pull keeps only its own, with no
// UNIQUE failure and neither file touched.
func TestSyncSameFileTwoBindings(t *testing.T) {
	dir := t.TempDir()
	wres, err := project.Init(dir, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	root := wres.Project
	root.Workspace = root.ID
	pres, err := project.Init(filepath.Join(dir, "projects", "alpha"), "alpha", "AL")
	if err != nil {
		t.Fatal(err)
	}
	alpha := pres.Project
	alpha.Workspace = root.ID
	if alpha.IsWorkspace() || !root.IsWorkspace() {
		t.Fatal("binding roles wrong")
	}

	rootArt, err := artifact.New(root, "free", "Same name", "tester")
	if err != nil {
		t.Fatal(err)
	}
	alphaArt, err := artifact.New(alpha, "free", "Same name", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if rootArt.File != alphaArt.File || rootArt.ID == alphaArt.ID {
		t.Fatalf("setup: %s/%s %s/%s", rootArt.File, rootArt.ID, alphaArt.File, alphaArt.ID)
	}
	// Distinct bodies, so an overwrite would show.
	rootBody, alphaBody := "root body\n", "alpha body\n"
	if err := os.WriteFile(rootArt.Path, []byte(rootBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alphaArt.Path, []byte(alphaBody), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, b := range []*project.Project{root, alpha} {
		if _, err := artifact.Reconcile(b); err != nil {
			t.Fatal(err)
		}
	}

	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	ctx := context.Background()

	if _, err := Run(ctx, cl, root); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, cl, alpha); err != nil {
		t.Fatal(err)
	}
	all := f.pushedChanges(t)
	if len(all) != 2 {
		t.Fatalf("pushed artifact rows: %d", len(all))
	}
	for _, ch := range all {
		var row map[string]any
		_ = json.Unmarshal(ch.Row, &row)
		pid, has := row["projectId"]
		switch ch.ID {
		case rootArt.ID:
			if has {
				t.Fatalf("root artifact pushed with projectId %v", pid)
			}
		case alphaArt.ID:
			if pid != alpha.ID {
				t.Fatalf("alpha artifact pushed with projectId %v, want %s", pid, alpha.ID)
			}
		}
	}

	// Both caches pull both rows, twice (a repeat sync must stay quiet too).
	for round := 0; round < 2; round++ {
		for _, b := range []*project.Project{alpha, root} {
			f.queue(all)
			r, err := Run(ctx, cl, b)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Notes) != 0 {
				t.Fatalf("%s round %d notes: %v", b.Slug, round, r.Notes)
			}
			if r.Applied != 1 {
				t.Fatalf("%s round %d applied %d, want only its own", b.Slug, round, r.Applied)
			}
		}
	}
	check := func(b *project.Project, wantID, wantBody string) {
		t.Helper()
		list, err := artifact.List(b)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].ID != wantID {
			t.Fatalf("%s cache: %+v", b.Slug, list)
		}
		got, err := os.ReadFile(list[0].Path)
		if err != nil || string(got) != wantBody {
			t.Fatalf("%s file: %q %v", b.Slug, got, err)
		}
	}
	check(root, rootArt.ID, rootBody)
	check(alpha, alphaArt.ID, alphaBody)
}

// A cache from before GRS-26 that took a workspace-level row in drops it on
// the next pull of that row (the server keeps it), so it is never pushed back
// under the project's id. A cached copy whose body differs from the server's
// (the root edited it since) keeps its file and is re-adopted as the
// project's own.
func TestSyncEvictsForeignArtifact(t *testing.T) {
	dir := t.TempDir()
	wres, err := project.Init(dir, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	root := wres.Project
	root.Workspace = root.ID
	pres, err := project.Init(filepath.Join(dir, "projects", "alpha"), "alpha", "AL")
	if err != nil {
		t.Fatal(err)
	}
	alpha := pres.Project
	alpha.Workspace = root.ID

	clean, err := artifact.New(root, "free", "Clean copy", "tester")
	if err != nil {
		t.Fatal(err)
	}
	edited, err := artifact.New(root, "idea", "Edited copy", "tester")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	ctx := context.Background()
	if _, err := Run(ctx, cl, root); err != nil {
		t.Fatal(err)
	}
	ws := f.pushedChanges(t)

	// Seed alpha the way the old rule did: rows and files copied in.
	db, err := store.Open(alpha)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(alpha.ArtifactsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*artifact.Artifact{clean, edited} {
		body, _ := os.ReadFile(a.Path)
		if a.ID == edited.ID {
			body = append(body, []byte("\nolder cached copy\n")...)
		}
		// The cache's row agrees with its file, so Reconcile leaves it be.
		localHash := artifact.Hash(string(body))
		if err := os.WriteFile(filepath.Join(alpha.ArtifactsDir(), a.File), body, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO artifacts (id, prefix, slug, file, title, status, created_at, created_by, updated_at, body_hash, file_present, shape_ok, shape_errors)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, '')`, a.ID, a.Prefix, a.Slug, a.File, a.Title, a.Status,
			"2020-01-01T00:00:00Z", "tester", "2020-01-01T00:00:00Z", localHash); err != nil {
			t.Fatal(err)
		}
	}
	// It had synced before: the pulled rows are not new local changes.
	if err := setState(db, "pushed_at", store.Now()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	f.queue(ws)
	r, err := Run(ctx, cl, alpha)
	if err != nil {
		t.Fatal(err)
	}
	if r.Applied != 0 || len(r.Notes) != 1 || !strings.Contains(r.Notes[0], edited.File) {
		t.Fatalf("applied %d notes %v", r.Applied, r.Notes)
	}
	if _, err := os.Stat(filepath.Join(alpha.ArtifactsDir(), clean.File)); !os.IsNotExist(err) {
		t.Fatalf("clean foreign copy left: %v", err)
	}
	if _, err := artifact.Reconcile(alpha); err != nil {
		t.Fatal(err)
	}
	list, err := artifact.List(alpha)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].File != edited.File || list[0].ID == edited.ID {
		t.Fatalf("alpha after evict: %+v", list)
	}
	// The root still has both, untouched.
	if rl, _ := artifact.List(root); len(rl) != 2 {
		t.Fatalf("root list: %+v", rl)
	}
}
