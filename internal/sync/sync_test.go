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
