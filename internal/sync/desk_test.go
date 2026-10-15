package sync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
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

// GRS-48: the API sends a desk item this person may not see as a delete,
// and the cache drops it. A cache filled before the API had desk privacy
// pulls from the start once, when it first hears "desk visible", so items
// it may no longer see come down as deletes too.
func TestSyncDeskPrivacy(t *testing.T) {
	res, err := project.Init(t.TempDir(), "deskpriv", "DP")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID
	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)

	const theirs = "01a130c3-0000-7000-8000-00000000d001"
	b, _ := json.Marshal(map[string]any{"id": theirs, "type": "info", "from": "bob-claude", "to": "zed", "text": "bob's, for zed", "state": "open",
		"author": "user-bob", "createdAt": "2026-10-14T10:00:00Z", "updatedAt": "2026-10-14T10:00:01Z"})
	f.queue([]api.Change{{Seq: 1, Table: "desk_items", ID: theirs, Op: "upsert", Row: b}})
	if _, err := Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	has := func() bool {
		items, err := desk.List(p, desk.Filter{All: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.ID == theirs {
				return true
			}
		}
		return false
	}
	if !has() {
		t.Fatal("an older API's row did not land")
	}

	// The API gains desk privacy: one pull from the start.
	f.mu.Lock()
	f.deskVisible = true
	f.cursors = nil
	f.mu.Unlock()
	r, err := Run(context.Background(), cl, p)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	cursors := append([]string(nil), f.cursors...)
	f.mu.Unlock()
	if len(cursors) < 2 || cursors[0] == "0" || cursors[1] != "0" || len(r.Notes) == 0 {
		t.Fatalf("no pull from the start: cursors %v notes %v", cursors, r.Notes)
	}

	// From the start, the API sends the item as a delete: the cache drops it.
	f.queue([]api.Change{{Seq: 2, Table: "desk_items", ID: theirs, Op: "upsert", Deleted: true}})
	f.mu.Lock()
	f.cursors = nil
	f.mu.Unlock()
	if r, err = Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Fatal("a desk item the API hides is still in the cache")
	}
	// Once only.
	for _, n := range r.Notes {
		if strings.HasPrefix(n, "desk privacy") {
			t.Fatalf("pulled from the start again: %v", r.Notes)
		}
	}
}
