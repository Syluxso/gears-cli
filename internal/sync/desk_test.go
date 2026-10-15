package sync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/desk"
	"github.com/Syluxso/gears-cli/internal/project"
)

// GRS-35: a desk item's assignment goes up on push (never its author: the
// API sets that from the token), and a pulled row brings author and
// assignedTo down, null assignment as "".
func TestSyncDeskAuthorAndAssignment(t *testing.T) {
	dir := t.TempDir()
	res, err := project.Init(dir, "desksync", "DS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID

	sent, err := desk.SendAssigned(p, "work", "claude-local", "", "review this", "", "user-bob")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := desk.Send(p, "info", "claude-local", "", "fyi", "")
	if err != nil {
		t.Fatal(err)
	}

	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	if _, err := Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	if len(f.pushes) != 1 {
		t.Fatalf("pushes: %d", len(f.pushes))
	}
	got := map[string]map[string]any{}
	for _, row := range f.pushes[0].Rows["desk_items"] {
		m := row.(map[string]any)
		got[m["id"].(string)] = m
	}
	if got[sent.ID]["assignedTo"] != "user-bob" || got[plain.ID]["assignedTo"] != "" {
		t.Fatalf("pushed assignedTo: %v", got)
	}
	for id, m := range got {
		if _, ok := m["author"]; ok {
			t.Fatalf("push sent an author for %s: %v", id, m)
		}
	}

	// The API answers with its author; one row is unassigned elsewhere.
	row := func(id, typ, text string, assigned any) api.Change {
		b, _ := json.Marshal(map[string]any{"id": id, "type": typ, "from": "claude-local", "text": text, "state": "open",
			"author": "user-alice", "assignedTo": assigned, "createdAt": "2026-10-14T10:00:00Z", "updatedAt": "2026-10-14T10:00:01Z"})
		return api.Change{Seq: 1, Table: "desk_items", ID: id, Op: "upsert", Row: b}
	}
	f.queue([]api.Change{row(sent.ID, "work", "review this", nil), row(plain.ID, "info", "fyi", "user-carol")})
	if _, err := Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	items, err := desk.List(p, desk.Filter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]desk.Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if it := byID[sent.ID]; it.Author != "user-alice" || it.AssignedTo != "" {
		t.Fatalf("pulled %s: %+v", sent.ID, it)
	}
	if it := byID[plain.ID]; it.Author != "user-alice" || it.AssignedTo != "user-carol" {
		t.Fatalf("pulled %s: %+v", plain.ID, it)
	}
}
